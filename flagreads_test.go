package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dispatchers maps a command with subcommands to where it is dispatched.
var dispatchers = map[string][2]string{
	"agent":    {"cmd_agent.go", "agentCmd"},
	"config":   {"cmd_auth.go", "configCmd"},
	"eval":     {"cmd_eval.go", "evalCmd"},
	"flow":     {"cmd_flow.go", "flowCmd"},
	"job":      {"cmd_job.go", "jobCmd"},
	"memory":   {"cmd_memory.go", "memoryCmd"},
	"model":    {"cmd_model.go", "modelCmd"},
	"schedule": {"cmd_schedule.go", "scheduleCmd"},
	"skill":    {"cmd_skill.go", "skillCmd"},
	"thread":   {"cmd_thread.go", "threadCmd"},
	"tool":     {"cmd_tool.go", "toolCmd"},
}

// plainCommands maps a command without subcommands to its function.
var plainCommands = map[string]string{
	"chat": "chatCmd", "feedback": "feedbackCmd", "guide": "guideCmd", "install": "installCmd",
	"login": "loginCmd", "setup": "setupCmd", "spend": "spendCmd",
	"uninstall": "uninstallCmd", "update": "updateCmd", "whoami": "whoamiCmd",
	"help-json": "helpJSON", "version": "",
}

type flagReader map[string]*ast.FuncDecl

func newFlagReader(t *testing.T) flagReader {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	funcs := flagReader{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			if f, ok := decl.(*ast.FuncDecl); ok && f.Recv == nil {
				funcs[f.Name.Name] = f
			}
		}
	}
	return funcs
}

// collect adds every flag read under node, following calls into the package.
func (r flagReader) collect(node ast.Node, seen, flags map[string]bool) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok || len(v.Args) == 0 {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "fl" {
				return true
			}
			switch sel.Sel.Name {
			case "str", "has", "intVal", "list", "floatPtr":
				if lit, ok := v.Args[0].(*ast.BasicLit); ok {
					name, _ := strconv.Unquote(lit.Value)
					flags["--"+name] = true
				}
			}
		case *ast.Ident:
			if f, ok := r[v.Name]; ok && !seen[v.Name] {
				seen[v.Name] = true
				r.collect(f.Body, seen, flags)
			}
		}
		return true
	})
}

// reads returns the flags one subcommand reads. An empty sub means any of
// them. Hidden subcommands ("_exec") are not part of the contract.
func (r flagReader) reads(command, sub string) map[string]bool {
	flags, seen := map[string]bool{}, map[string]bool{}
	at, hasSubs := dispatchers[command]
	if !hasSubs {
		if f, ok := r[plainCommands[command]]; ok {
			r.collect(f.Body, seen, flags)
		}
		return flags
	}
	for _, stmt := range r[at[1]].Body.List {
		sw, ok := stmt.(*ast.SwitchStmt)
		if !ok {
			r.collect(stmt, seen, flags)
			continue
		}
		for _, c := range sw.Body.List {
			clause := c.(*ast.CaseClause)
			hidden, matches := false, sub == ""
			for _, expr := range clause.List {
				if lit, ok := expr.(*ast.BasicLit); ok {
					name, _ := strconv.Unquote(lit.Value)
					hidden = hidden || strings.HasPrefix(name, "_")
					matches = matches || name == sub
				}
			}
			if hidden || !matches || clause.List == nil {
				continue
			}
			for _, s := range clause.Body {
				r.collect(s, seen, flags)
			}
		}
	}
	return flags
}
