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
		if strings.HasPrefix(v, "ahub ") {
			// A gap or " # " starts the explanation.
			return []string{columnGap.Split(v, 2)[0]}
		}
		for _, m := range codeSpan.FindAllStringSubmatch(v, -1) {
			found = append(found, m[1])
		}
	}
	return found
}

// drift says why a command line would not work as written, one reason each:
// an unknown command, or a flag that this subcommand never reads.
func drift(line string, commands map[string]any, reader flagReader) []string {
	var problems []string
	// Quoted values may hold pipes and other commands' flags.
	for _, segment := range pipeline.Split(quoted.ReplaceAllString(line, "Q"), -1) {
		words := strings.Fields(segment)
		if len(words) < 2 || words[0] != "ahub" || words[1] == "..." || strings.HasPrefix(words[1], "%") ||
			strings.HasPrefix(words[1], "<") { // a placeholder for any command
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
		subcommand := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "[") && len(rest) > 0 &&
			!strings.HasPrefix(strings.TrimPrefix(rest[0], "["), "-") && rest[0] != "..." {
			// Usage text writes alternatives as "list|show" or "[show|set <n>]".
			alternatives := strings.Split(strings.Trim(rest[0], "[]"), "|")
			for _, alt := range alternatives {
				if !slices.Contains(strings.Split(args[0], "|"), alt) {
					problems = append(problems, "no subcommand "+name+" "+alt)
				}
			}
			if len(alternatives) == 1 {
				subcommand = alternatives[0]
			}
		}
		read := reader.reads(name, subcommand)
		for _, w := range rest {
			flag, _, _ := strings.Cut(strings.Trim(w, "[]"), "=")
			if strings.HasPrefix(flag, "--") && !read[flag] && !slices.Contains(globalFlags, flag) {
				problems = append(problems, strings.TrimSpace(name+" "+subcommand)+" does not read "+flag)
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
	checkLines(t, lines)
}

func checkLines(t *testing.T, lines []string) {
	t.Helper()
	commands := catalog()["commands"].(map[string]any)
	reader := newFlagReader(t)
	for _, line := range lines {
		for _, p := range drift(line, commands, reader) {
			t.Errorf("%s\n\tin: %s", p, line)
		}
	}
}
