package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var columnGap = regexp.MustCompile(`\s{2,}|\s#\s`)

// readmeInvocations returns the ahub lines of the README's sh blocks (with
// continuations joined) and its `ahub ...` spans.
func readmeInvocations(text string) []string {
	var found []string
	inShell, inFence, pending := false, false, ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			inShell = !inFence && line == "```sh"
			inFence = !inFence
			continue
		}
		if !inFence {
			for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
				found = append(found, m[1])
			}
			continue
		}
		if !inShell {
			continue
		}
		if joined, more := strings.CutSuffix(line, "\\"); more {
			pending += joined
			continue
		}
		full := strings.TrimSpace(pending + line)
		pending = ""
		if strings.HasPrefix(full, "ahub ") {
			command, _, _ := strings.Cut(full, " # ")
			found = append(found, command)
		}
	}
	return found
}

func TestReadmeOnlyShowsCommandsAndFlagsThatWork(t *testing.T) {
	text, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := readmeInvocations(string(text))
	if len(lines) < 40 {
		t.Fatalf("found only %d command lines in the README; the extractor is broken", len(lines))
	}
	checkLines(t, lines)
}

// sourceInvocations returns the commands the binary itself tells a caller to
// run: suggestions and usage text inside string literals.
func sourceInvocations(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, _ := strconv.Unquote(lit.Value)
			for _, m := range codeSpan.FindAllStringSubmatch(value, -1) {
				found = append(found, m[1])
			}
			for _, line := range strings.Split(value, "\n") {
				line = strings.TrimSpace(line)
				// "ahub x - ..." is a sentence about x; a gap starts a description.
				if strings.HasPrefix(line, "ahub ") && !strings.Contains(line, " - ") {
					found = append(found, columnGap.Split(line, 2)[0])
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestMessagesOnlySuggestCommandsAndFlagsThatWork(t *testing.T) {
	lines := sourceInvocations(t)
	if len(lines) < 30 {
		t.Fatalf("found only %d command lines in the source; the extractor is broken", len(lines))
	}
	checkLines(t, lines)
}
