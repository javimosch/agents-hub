package flow

import (
	"fmt"
	"slices"
	"strings"
)

// ToolPolicy is what this machine lets a model run.
//
// A prefix says how a command starts. That alone is not a boundary: with
// `git log` permitted, `git log --output=<path>` writes a file. So every
// further argument must also match a pattern the operator listed for that
// prefix, and a prefix with no patterns takes no arguments.
type ToolPolicy struct {
	// Commands are the permitted prefixes, such as "gh pr list".
	Commands []string
	// Args lists, per prefix, the patterns a further argument may match.
	Args map[string][]string
}

// Resolve returns the argv to run for a command and its extra arguments, or
// why the policy refuses them. command must be exactly a permitted prefix;
// that prefix's patterns are the ones applied.
func (p ToolPolicy) Resolve(command string, args []string) ([]string, error) {
	prefixes, err := ParsePrefixes(p.Commands)
	if err != nil {
		return nil, err
	}
	want := strings.Join(strings.Fields(command), " ")
	i := slices.IndexFunc(prefixes, func(x Prefix) bool { return x.Raw == want })
	if i < 0 {
		if len(prefixes) == 0 {
			return nil, fmt.Errorf("no commands are permitted on this machine; " +
				"add one with `ahub config set --allow-tool-cmd '<prefix>'`")
		}
		if m, ok := Match(prefixes, strings.Fields(want)); ok {
			return nil, fmt.Errorf("%q has words after %q; give each one as a separate argument", command, m.Raw)
		}
		return nil, fmt.Errorf("%q is not permitted here. Permitted commands: %s",
			command, Names(prefixes))
	}
	prefix := prefixes[i]
	patterns := p.Args[prefix.Raw]
	for _, arg := range args {
		if !slices.ContainsFunc(patterns, func(pattern string) bool { return matchArg(pattern, arg) }) {
			return nil, fmt.Errorf("argument %q is not permitted after %q. It may add: %s. "+
				"An operator can permit more with `ahub config set --allow-tool-arg '<prefix> <pattern>'`",
				arg, prefix.Raw, patternList(patterns))
		}
	}
	return append(append([]string{}, prefix.Fields...), args...), nil
}

// Describe lists each permitted command with what may follow it.
func (p ToolPolicy) Describe() string {
	prefixes, err := ParsePrefixes(p.Commands)
	if err != nil {
		return ""
	}
	out := make([]string, 0, len(prefixes))
	for _, x := range prefixes {
		if patterns := p.Args[x.Raw]; len(patterns) > 0 {
			out = append(out, x.Raw+" (may add: "+patternList(patterns)+")")
		} else {
			out = append(out, x.Raw+" (no arguments)")
		}
	}
	return strings.Join(out, " | ")
}

// SplitRule reads "<permitted prefix> <pattern>...", taking the longest
// permitted prefix the rule starts with.
func (p ToolPolicy) SplitRule(rule string) (prefix string, patterns []string, err error) {
	prefixes, err := ParsePrefixes(p.Commands)
	if err != nil {
		return "", nil, err
	}
	fields := strings.Fields(rule)
	var best Prefix
	for _, x := range prefixes {
		if _, ok := Match([]Prefix{x}, fields); ok && len(x.Fields) > len(best.Fields) {
			best = x
		}
	}
	if best.Raw == "" {
		return "", nil, fmt.Errorf("%q does not start with a permitted command", rule)
	}
	if len(fields) == len(best.Fields) {
		return "", nil, fmt.Errorf("%q names no argument pattern after %q", rule, best.Raw)
	}
	return best.Raw, fields[len(best.Fields):], nil
}

func patternList(patterns []string) string {
	if len(patterns) == 0 {
		return "no arguments"
	}
	return strings.Join(patterns, " ")
}

// matchArg matches one whole argument. "*" stands for any run of characters.
//
// An argument starting with "-" is only matched by a pattern that starts with
// "-" itself: "*" is for values, and must never let a flag through.
func matchArg(pattern, arg string) bool {
	if strings.HasPrefix(arg, "-") && !strings.HasPrefix(pattern, "-") {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return arg == pattern
	}
	rest, ok := strings.CutPrefix(arg, parts[0])
	if !ok {
		return false
	}
	for _, part := range parts[1 : len(parts)-1] {
		if _, rest, ok = strings.Cut(rest, part); !ok {
			return false
		}
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}
