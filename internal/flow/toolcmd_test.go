package flow

import (
	"slices"
	"strings"
	"testing"
)

func TestToolPolicyOnlyAcceptsListedArguments(t *testing.T) {
	policy := ToolPolicy{
		Commands: []string{"git log", "gh pr list", "echo", "echo hello"},
		Args: map[string][]string{
			"git log":    {"--oneline", "--since=*", "-n", "*"},
			"gh pr list": {"--state"},
			"echo":       {"-*"},
		},
	}
	cases := []struct {
		name    string
		command string
		args    []string
		ok      bool
	}{
		{"no arguments", "git log", nil, true},
		{"an exact flag", "git log", []string{"--oneline"}, true},
		{"a wildcard flag value, spaces and all", "git log", []string{"--since=1 week ago"}, true},
		{"a value matched by *", "git log", []string{"-n", "5"}, true},
		{"a flag nobody listed", "git log", []string{"--output=/tmp/x"}, false},
		{"one bad argument among good ones", "git log", []string{"--oneline", "--output=/tmp/x"}, false},
		{"* never matches a flag", "git log", []string{"--all"}, false},
		{"* never matches a short flag", "git log", []string{"-p"}, false},
		{"an exact pattern is not a prefix", "git log", []string{"--oneline=x"}, false},
		{"values need their own pattern", "gh pr list", []string{"--state", "open"}, false},
		{"a pattern starting - opts into flags", "echo", []string{"-n"}, true},
		{"but a -* pattern does not match values", "echo", []string{"hi"}, false},
		{"the longest prefix decides: it has no rules", "echo hello", []string{"-n"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, err := policy.Resolve(c.command, c.args)
			if (err == nil) != c.ok {
				t.Fatalf("Resolve(%q, %q): got err %v, want allowed=%v", c.command, c.args, err, c.ok)
			}
			if want := append(strings.Fields(c.command), c.args...); c.ok && !slices.Equal(argv, want) {
				t.Errorf("argv: got %q, want %q", argv, want)
			}
		})
	}
}

func TestAPrefixWithNoRulesAcceptsNoArguments(t *testing.T) {
	policy := ToolPolicy{Commands: []string{"git log"}}
	if _, err := policy.Resolve("git log", nil); err != nil {
		t.Errorf("the bare command: got %v, want allowed", err)
	}
	_, err := policy.Resolve("git log", []string{"--oneline"})
	if err == nil || !strings.Contains(err.Error(), "--allow-tool-arg") {
		t.Errorf("got %v, want a refusal that says how to permit it", err)
	}
}

// Extra words used to be dropped, so the model was told a command ran that
// had not.
func TestWordsAfterThePrefixAreRefusedNotDropped(t *testing.T) {
	policy := ToolPolicy{Commands: []string{"git log"}, Args: map[string][]string{"git log": {"--oneline"}}}
	if argv, err := policy.Resolve("git log --oneline", nil); err == nil {
		t.Errorf("got argv %q, want a refusal", argv)
	}
}
