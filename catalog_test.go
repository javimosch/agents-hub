package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// switchCases reads, from source, the string cases of the switch inside fn:
// one slice per clause, the canonical spelling first and its aliases after.
// Dispatch cannot be probed by running it: known commands act (install,
// update), so the source is the only safe oracle.
func switchCases(t *testing.T, file, fn string) [][]string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var clauses [][]string
	for _, decl := range parsed.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok || f.Name.Name != fn {
			continue
		}
		var first *ast.SwitchStmt
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SwitchStmt); ok && first == nil {
				first = s
			}
			return first == nil
		})
		if first == nil {
			break
		}
		for _, stmt := range first.Body.List {
			var names []string
			for _, expr := range stmt.(*ast.CaseClause).List {
				if lit, ok := expr.(*ast.BasicLit); ok {
					name, _ := strconv.Unquote(lit.Value)
					// "" is the default action; "-x" and "_x" are not commands.
					if name != "" && !strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "_") {
						names = append(names, name)
					}
				}
			}
			if len(names) > 0 {
				clauses = append(clauses, names)
			}
		}
	}
	if len(clauses) == 0 {
		t.Fatalf("no switch cases found in %s %s", file, fn)
	}
	return clauses
}

// compareWithDispatch fails when the catalog lists something dispatch refuses,
// or leaves out something dispatch accepts.
func compareWithDispatch(t *testing.T, what string, listed []string, clauses [][]string) {
	t.Helper()
	var all []string
	for _, clause := range clauses {
		all = append(all, clause...)
		if !slices.ContainsFunc(clause, func(n string) bool { return slices.Contains(listed, n) }) {
			t.Errorf("%s: dispatch accepts %q, help-json does not list it", what, clause[0])
		}
	}
	for _, name := range listed {
		if !slices.Contains(all, name) {
			t.Errorf("%s: help-json lists %q, dispatch does not accept it", what, name)
		}
	}
}

func TestHelpJSONListsExactlyTheCommandsThatDispatch(t *testing.T) {
	commands := catalog()["commands"].(map[string]any)
	var listed []string
	for name := range commands {
		listed = append(listed, name)
	}
	var clauses [][]string
	for _, c := range switchCases(t, "main.go", "main") {
		if c[0] != "help" { // prints usage; not part of the JSON contract
			clauses = append(clauses, c)
		}
	}
	compareWithDispatch(t, "ahub", listed, clauses)
}

func TestHelpJSONListsExactlyTheSubcommandsThatDispatch(t *testing.T) {
	commands := catalog()["commands"].(map[string]any)
	for name, at := range dispatchers {
		t.Run(name, func(t *testing.T) {
			entry, ok := commands[name].(map[string]any)
			if !ok {
				t.Fatalf("help-json has no %s command", name)
			}
			listed := strings.Split(entry["args"].([]string)[0], "|")
			compareWithDispatch(t, name, listed, switchCases(t, at[0], at[1]))
		})
	}
}

func TestHelpJSONListsExactlyTheFlagsEachCommandReads(t *testing.T) {
	reader := newFlagReader(t)
	for name, entry := range catalog()["commands"].(map[string]any) {
		t.Run(name, func(t *testing.T) {
			listed := []string{}
			for _, f := range entry.(map[string]any)["flags"].([]string) {
				listed = append(listed, strings.Fields(f)[0])
			}
			read := reader.reads(name, "")
			for flag := range read {
				if !slices.Contains(listed, flag) && !slices.Contains(globalFlags, flag) {
					t.Errorf("%s reads %s, help-json does not list it", name, flag)
				}
			}
			for _, flag := range listed {
				if !read[flag] && !slices.Contains(globalFlags, flag) {
					t.Errorf("help-json lists %s %s, the command never reads it", name, flag)
				}
			}
		})
	}
}
