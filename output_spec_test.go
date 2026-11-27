package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Asking for help once ran the command: `update --help` replaced the binary.
func TestHelpNeverRunsTheCommand(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	prefix := t.TempDir()
	installed := filepath.Join(prefix, "ahub")
	if err := os.WriteFile(installed, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(cliEnv(t, server.URL), "AHUB_UPDATE_URL="+server.URL+"/ahub.json")
	for _, args := range [][]string{
		{"update", "--help"}, {"update", "-h"},
		{"uninstall", "--prefix", prefix, "--help"},
		{"agent", "list", "--help"}, {"chat", "--help"},
	} {
		r := runCLI(t, os.Args[0], env, args...)
		if r.code != 0 || !strings.Contains(r.stdout, `"flags"`) {
			t.Errorf("%v: got exit %d and %q, want the command's help", args, r.code, r.stdout)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("help made %d requests, want 0", n)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Error("`uninstall --help` removed the binary")
	}
}

// An ignored flag is how `thread list --assistant` returned everyone's threads.
func TestUnknownFlagsAreRefused(t *testing.T) {
	for name, args := range map[string][]string{
		"a flag nobody reads":              {"tool", "list", "--nope"},
		"another command's flag":           {"tool", "list", "--agent", "x"},
		"a value that looks like a flag":   {"config", "set", "--allow-tool-arg", "--oneline"},
		"a typo that would drop a setting": {"config", "set", "--daily-token-budge", "5"},
	} {
		t.Run(name, func(t *testing.T) {
			wantError(t, run(t, args...), 80, "unknown_flag")
		})
	}
}

func TestStandardAgentFlagsAreAccepted(t *testing.T) {
	for _, flag := range []string{"--json", "--no-interactive", "--yes", "-y"} {
		if r := run(t, "tool", "list", flag); r.code != 0 {
			t.Errorf("tool list %s: got exit %d: %s", flag, r.code, r.stderr)
		}
	}
}

func TestEveryFailureIsATypedError(t *testing.T) {
	t.Run("no command", func(t *testing.T) {
		wantError(t, run(t), 80, "unknown_command")
	})
	t.Run("a bad subcommand is reported before authentication", func(t *testing.T) {
		env := append(cliEnv(t, "http://127.0.0.1:1"), "AHUB_TOKEN=")
		for _, command := range []string{"agent", "thread"} {
			wantError(t, runCLI(t, os.Args[0], env, command, "bogus"), 80, "unknown_command")
		}
	})
}

// Only out.Fail writes the error envelope, so nothing else may exit non-zero.
func TestNothingExitsWithoutATypedError(t *testing.T) {
	for _, file := range []string{"main.go", "cmd_job.go"} {
		raw := readFile(t, file)
		if strings.Contains(raw, "os.Exit(out.") {
			t.Errorf("%s exits with a code and no error body", file)
		}
	}
}

var envName = regexp.MustCompile(`^(AHUB_[A-Z_]+|FEEDBACK_[A-Z_]+)$`)

func TestHelpJSONListsEveryEnvironmentVariable(t *testing.T) {
	listed := catalog()["env"].([]string)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, _ := strconv.Unquote(lit.Value); envName.MatchString(v) && !slices.Contains(listed, v) {
					t.Errorf("%s reads %s, help-json does not list it", path, v)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
