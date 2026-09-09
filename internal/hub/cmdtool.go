package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/provider"
)

// maxCmdOutput caps what a command may hand back to a model. The result goes
// straight into the next prompt and is paid for on every subsequent step of
// the loop, so an uncapped `find /` is a bill as much as a mistake.
const maxCmdOutput = 4000

// Prefix is one command shape an operator has permitted a model to run.
//
// It is a prefix and not a bare command name because the argv author differs
// from a flow's: there, a human wrote `git -C /repo status --porcelain`; here
// a model writes the arguments, and allowlisting `git` would permit
// `git push --force`. The model may append to a prefix, never alter it.
type Prefix struct {
	Raw    string
	Fields []string
}

// ParsePrefixes turns the configured strings into prefixes, refusing any that
// is empty or that starts with a flag.
func ParsePrefixes(raw []string) ([]Prefix, error) {
	out := make([]Prefix, 0, len(raw))
	for _, r := range raw {
		fields := strings.Fields(r)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "-") {
			return nil, fmt.Errorf("%q does not start with a command", r)
		}
		out = append(out, Prefix{Raw: strings.Join(fields, " "), Fields: fields})
	}
	return out, nil
}

// match finds the prefix a requested command belongs to. The comparison is on
// whole fields, so "git logger" cannot pass as "git log".
func match(prefixes []Prefix, want string) (Prefix, bool) {
	fields := strings.Fields(want)
	for _, p := range prefixes {
		if len(fields) != len(p.Fields) {
			continue
		}
		same := true
		for i := range fields {
			if fields[i] != p.Fields[i] {
				same = false
				break
			}
		}
		if same {
			return p, true
		}
	}
	return Prefix{}, false
}

// cmdTool exposes the allowed prefixes to a model.
//
// There is no second --allow-writes gate here, and that is deliberate rather
// than an omission: the prefix list IS the gate, and it is far more specific
// than a blanket flag. It is empty by default, so the tool does not exist
// until an operator writes down exactly which command shapes are permitted --
// and a flag that must be passed even to run `git log` would only teach
// people that the flag means nothing.
func cmdTool(prefixes []Prefix, timeout time.Duration) Tool {
	allowed := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		allowed = append(allowed, p.Raw)
	}
	list := strings.Join(allowed, " | ")
	return Tool{
		Spec: provider.Tool{
			Name: "cmd",
			Description: "Run one of the commands this machine permits, and read its output. " +
				"You may append arguments to a permitted command but never change it. " +
				"Permitted commands: " + list + ". " +
				"The result carries the exit code: 0 succeeded, and every tool here follows the " +
				"same convention -- 105 means retry, 82 means your arguments were wrong, " +
				"92 means the thing was not found.",
			Parameters: object(map[string]any{
				"command": prop("string", "Exactly one of the permitted commands, copied verbatim: "+list),
				"args":    arrayProp("string", "Extra arguments to append. Each element is one argument; they are never re-parsed by a shell."),
			}, "command"),
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			want := str(args, "command")
			p, ok := match(prefixes, want)
			if !ok {
				return "", fmt.Errorf("%q is not permitted here. Permitted commands: %s", want, list)
			}
			extra, err := strList(args, "args")
			if err != nil {
				return "", err
			}
			argv := append(append([]string{}, p.Fields[1:]...), extra...)
			out, err := flow.RunCmd(ctx,
				flow.Limits{Commands: []string{p.Fields[0]}, Timeout: timeout},
				p.Fields[0], argv, "")
			if err != nil {
				return "", err
			}
			return renderCmdResult(out), nil
		},
	}
}

// renderCmdResult gives the model the exit code and both streams, trimmed.
// The code comes first because it is the part a model should branch on, and
// the part it will otherwise ignore in favour of guessing from the text.
func renderCmdResult(o *flow.Output) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %s\n", strconv.Itoa(o.Code))
	if s := trimTo(o.Text, maxCmdOutput); s != "" {
		fmt.Fprintf(&b, "stdout:\n%s\n", s)
	}
	if s := trimTo(o.Err, 1000); s != "" {
		fmt.Fprintf(&b, "stderr:\n%s\n", s)
	}
	if o.Text == "" && o.Err == "" {
		b.WriteString("(no output)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[...truncated]"
}

func strList(args json.RawMessage, key string) ([]string, error) {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, err
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		// A model that sends a single string instead of an array is making an
		// ordinary mistake, not an attack: one argument is a fine reading.
		if s, ok := raw.(string); ok {
			return []string{s}, nil
		}
		return nil, fmt.Errorf("args must be an array of strings")
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("every element of args must be a string")
		}
		out = append(out, s)
	}
	return out, nil
}

func arrayProp(itemType, desc string) map[string]any {
	return map[string]any{
		"type": "array", "description": desc,
		"items": map[string]any{"type": itemType},
	}
}
