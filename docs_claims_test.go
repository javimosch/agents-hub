package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/javimosch/agents-hub/internal/guide"
	"github.com/javimosch/agents-hub/internal/hub"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// callsUpdateNudge reports whether the CLI ever runs the passive update check.
func callsUpdateNudge(t *testing.T) bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Nudge" {
				called = true
			}
			return true
		})
	}
	return called
}

func TestUpdateNudgeIsOnlyAdvertisedIfItRuns(t *testing.T) {
	if callsUpdateNudge(t) {
		return
	}
	if strings.Contains(readFile(t, "README.md"), "passive nudge |") {
		t.Error("the README claims a passive nudge, but nothing calls it")
	}
	if slices.Contains(catalog()["env"].([]string), "AHUB_NO_NUDGE") {
		t.Error("help-json lists AHUB_NO_NUDGE, but there is no nudge to disable")
	}
}

func TestReadmeToolTableListsEveryTool(t *testing.T) {
	readme := readFile(t, "README.md")
	for _, tool := range hub.ToolCatalog() {
		if !strings.Contains(readme, "\n| `"+tool["name"].(string)+"`") {
			t.Errorf("the README tool table has no row for %s", tool["name"])
		}
	}
}

func TestDocsDoNotSayEveryToolUsesMemgraph(t *testing.T) {
	body, err := guide.Body("test")
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{
		"README.md": readFile(t, "README.md"),
		"guide":     body["model"].(map[string]any)["tools"].(string),
	}
	for name, text := range texts {
		for _, claim := range []string{"Every tool is memgraph-backed", "every one is backed by memgraph"} {
			if strings.Contains(text, claim) {
				t.Errorf("%s says %q; cmd and the job tools do not use memgraph", name, claim)
			}
		}
	}
}
