package flow

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// varRe matches {{name}} and {{secret.NAME}}. Whitespace inside the braces is
// tolerated because humans write these by hand.
var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}`)

// Refs lists every variable a step reads, for the validator.
func Refs(s Step) []string {
	seen := map[string]bool{}
	for _, field := range []string{s.Value, s.Text, s.Prompt, s.URL, s.Body, s.Stdin, s.Input} {
		for _, m := range varRe.FindAllStringSubmatch(field, -1) {
			seen[m[1]] = true
		}
	}
	for _, a := range s.Args {
		for _, m := range varRe.FindAllStringSubmatch(a, -1) {
			seen[m[1]] = true
		}
	}
	for _, v := range s.Headers {
		for _, m := range varRe.FindAllStringSubmatch(v, -1) {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Renderer substitutes variables. Secrets live in a separate namespace so a
// flow definition -- which is stored in bkn and may have been written by
// something other than a human -- can reference a credential by name without
// ever containing it.
type Renderer struct {
	Data    map[string]string
	Secrets map[string]string
}

// ErrUnset is returned when a template names something nothing provides. It
// fails rather than substituting an empty string: a URL with a silently empty
// path segment is a request to the wrong place, and a command with a silently
// empty argument is a different command.
type ErrUnset struct{ Name string }

func (e *ErrUnset) Error() string { return "{{" + e.Name + "}} is not set" }

// Render substitutes into one string.
func (r Renderer) Render(s string) (string, error) {
	var bad string
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if rest, ok := strings.CutPrefix(name, "secret."); ok {
			v, ok := r.Secrets[rest]
			if !ok && bad == "" {
				bad = name
			}
			return v
		}
		v, ok := r.Data[name]
		if !ok && bad == "" {
			bad = name
		}
		return v
	})
	if bad != "" {
		return "", &ErrUnset{Name: bad}
	}
	return out, nil
}

// RenderAll substitutes into a list, used for command arguments. Each
// argument is rendered whole and stays one argument: a value containing
// spaces cannot split itself into two, because nothing here ever re-parses a
// command line.
func (r Renderer) RenderAll(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		v, err := r.Render(s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Redact replaces every secret value with a marker, for anything that gets
// stored or printed. A run record is durable and readable; a credential that
// reaches it is a credential that has leaked.
func (r Renderer) Redact(s string) string {
	for name, v := range r.Secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "{{secret."+name+"}}")
		}
	}
	return s
}

// hostOf returns a URL's host, or "" when it is templated or unparseable.
func hostOf(raw string) string {
	if strings.Contains(raw, "{{") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// compare evaluates one branch. Numeric comparison is attempted first for gt
// and lt, falling back to string order, so "10" > "9" behaves the way whoever
// wrote the flow expected.
func compare(have, op, want string) (bool, error) {
	switch op {
	case OpEq:
		return have == want, nil
	case OpNe:
		return have != want, nil
	case OpContains:
		return strings.Contains(have, want), nil
	case OpEmpty:
		return strings.TrimSpace(have) == "", nil
	case OpNotEmpty:
		return strings.TrimSpace(have) != "", nil
	case OpGt, OpLt:
		hf, herr := parseNum(have)
		wf, werr := parseNum(want)
		if herr == nil && werr == nil {
			if op == OpGt {
				return hf > wf, nil
			}
			return hf < wf, nil
		}
		if op == OpGt {
			return have > want, nil
		}
		return have < want, nil
	}
	return false, fmt.Errorf("unknown operator %q", op)
}

func parseNum(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f, err
}
