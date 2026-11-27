package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/javimosch/agents-hub/internal/guide"
)

var (
	quoted   = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	codeSpan = regexp.MustCompile("`(ahub [^`]*)`")
	pipeline = regexp.MustCompile(`\|\||&&|\|`)
)

// globalFlags are accepted by every command (see parse in flags.go).
var globalFlags = []string{"--profile", "--server", "--bkn", "--token",
	"--provider", "--provider-url", "--model", "--human", "--stream"}

// guideInvocations returns every ahub command line the guide tells an agent
// to run: each "command" field, and each `ahub ...` span in its prose.
func guideInvocations(node any, key string) []string {
	var found []string
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			found = append(found, guideInvocations(child, k)...)
		}
	case []any:
		for _, child := range v {
			found = append(found, guideInvocations(child, key)...)
		}
	case string:
		if key == "command" {
			return []string{v}
		}
		for _, m := range codeSpan.FindAllStringSubmatch(v, -1) {
			found = append(found, m[1])
		}
	}
	return found
}

// drift says why the catalog does not accept a command line, one reason each.
func drift(line string, commands map[string]any) []string {
	var problems []string
	// Quoted values may hold pipes and other commands' flags.
	for _, segment := range pipeline.Split(quoted.ReplaceAllString(line, "Q"), -1) {
		words := strings.Fields(segment)
		if len(words) < 2 || words[0] != "ahub" || words[1] == "..." {
			continue
		}
		name := words[1]
		entry, ok := commands[name].(map[string]any)
		if !ok {
			problems = append(problems, "no command "+name)
			continue
		}
		args := entry["args"].([]string)
		rest := words[2:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "[") && len(rest) > 0 &&
			!strings.HasPrefix(strings.TrimPrefix(rest[0], "["), "-") && rest[0] != "..." {
			if !slices.Contains(strings.Split(args[0], "|"), rest[0]) {
				problems = append(problems, "no subcommand "+name+" "+rest[0])
				continue
			}
		}
		accepted := slices.Clone(globalFlags)
		for _, f := range entry["flags"].([]string) {
			accepted = append(accepted, strings.Fields(f)[0])
		}
		for _, w := range rest {
			w = strings.Trim(w, "[]")
			if flag, _, _ := strings.Cut(w, "="); strings.HasPrefix(flag, "--") && !slices.Contains(accepted, flag) {
				problems = append(problems, name+" has no flag "+flag)
			}
		}
	}
	return problems
}

func TestGuideOnlyNamesCommandsAndFlagsThatExist(t *testing.T) {
	body, err := guide.Body("test")
	if err != nil {
		t.Fatal(err)
	}
	lines := guideInvocations(body, "")
	if len(lines) < 40 {
		t.Fatalf("found only %d command lines in the guide; the extractor is broken", len(lines))
	}
	commands := catalog()["commands"].(map[string]any)
	for _, line := range lines {
		for _, p := range drift(line, commands) {
			t.Errorf("%s\n\tin: %s", p, line)
		}
	}
}
