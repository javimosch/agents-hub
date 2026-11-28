package flow

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A secret input is a value a run uses and never keeps: a password, a one-time
// code. That is only true if it cannot be copied, shown or handed to something
// that records it, so the places it may go are a closed list: an http body or
// header, a command's stdin, and a condition. What comes back is cleaned
// before anything else can read it.

const redacted = "[redacted]"

// SecretVars names the variables a flow collects as secrets.
func SecretVars(f *Flow) []string {
	var names []string
	for _, s := range f.Steps {
		if s.Secret && s.Type == TypeCollect && !slices.Contains(names, s.Var) {
			names = append(names, s.Var)
		}
	}
	slices.Sort(names)
	return names
}

func mentionsVar(text, name string) bool {
	for _, m := range varRe.FindAllStringSubmatch(text, -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

// secretMisuse says why a step may not run in a flow with these secrets. It is
// checked by the validator and again by the engine: a stored definition is
// data.
func secretMisuse(s Step, secrets []string) error {
	if s.Secret && s.Type != TypeCollect {
		return fmt.Errorf("only collect_input can be secret")
	}
	for _, name := range secrets {
		if s.Var == name && !(s.Type == TypeCollect && s.Secret) {
			return fmt.Errorf("writes %s, which is a secret input", name)
		}
		forbidden := map[string]string{
			"value": s.Value, "text": s.Text, "prompt": s.Prompt, "url": s.URL,
			"dir": s.Dir, "input": s.Input, "args": strings.Join(s.Args, "\x00"),
		}
		for field, text := range forbidden {
			if mentionsVar(text, name) {
				return fmt.Errorf("puts the secret %s in %s; it may go only in an http body or header, "+
					"a command's stdin or a condition", name, field)
			}
		}
	}
	return nil
}

// missingSecret returns the step that collects a secret this step needs and the
// run does not hold, which is the case after any pause.
func (e *Engine) missingSecret(run *Run, s Step, secrets []string) (Step, bool) {
	for _, name := range secrets {
		if _, held := run.Data[name]; held {
			continue
		}
		used := mentionsVar(s.Body, name) || mentionsVar(s.Stdin, name)
		for _, h := range s.Headers {
			used = used || mentionsVar(h, name)
		}
		for _, b := range s.When {
			used = used || b.Var == name || mentionsVar(b.Value, name)
		}
		if !used {
			continue
		}
		for _, c := range e.Flow.Steps {
			if c.Secret && c.Var == name {
				return c, true
			}
		}
	}
	return Step{}, false
}

// scrubber removes the secrets a run holds from text.
type scrubber []string

func newScrubber(run *Run, secrets []string) scrubber {
	var values scrubber
	for _, name := range secrets {
		if v := run.Data[name]; v != "" {
			values = append(values, v)
		}
	}
	return values
}

// text removes each secret as written, and as JSON writes it. A response that
// is JSON is decoded first, so that no encoder's choice of escapes hides it.
// What a service changes (a hash, base64, upper case) cannot be recognised.
func (sc scrubber) text(s string) string {
	if len(sc) == 0 || s == "" {
		return s
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if dec.Decode(&doc) == nil && !dec.More() {
		if clean, changed := sc.walk(doc); changed {
			if b, err := json.Marshal(clean); err == nil {
				s = string(b)
			}
		}
	}
	for _, v := range sc {
		html, _ := json.Marshal(v)
		for _, form := range []string{v, escapeJSON(v), string(html[1 : len(html)-1])} {
			s = strings.ReplaceAll(s, form, redacted)
		}
	}
	return s
}

func (sc scrubber) walk(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		clean := t
		for _, secret := range sc {
			clean = strings.ReplaceAll(clean, secret, redacted)
		}
		return clean, clean != t
	case []any:
		changed := false
		for i := range t {
			c, did := sc.walk(t[i])
			t[i], changed = c, changed || did
		}
		return t, changed
	case map[string]any:
		changed := false
		out := map[string]any{}
		for k, child := range t {
			key, keyDid := sc.walk(k)
			c, did := sc.walk(child)
			out[key.(string)] = c
			changed = changed || did || keyDid
		}
		return out, changed
	}
	return v, false
}

// forget drops the secrets from a run that is about to be kept, and cleans
// anything that still repeats them.
func forget(run *Run, secrets []string) {
	sc := newScrubber(run, secrets)
	for _, name := range secrets {
		delete(run.Data, name)
	}
	for k, v := range run.Data {
		run.Data[k] = sc.text(v)
	}
	for i := range run.Messages {
		run.Messages[i] = sc.text(run.Messages[i])
	}
	for i := range run.Trace {
		run.Trace[i].Detail = sc.text(run.Trace[i].Detail)
	}
	run.Error = sc.text(run.Error)
}
