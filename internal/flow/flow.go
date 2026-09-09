// Package flow is the deterministic half of the product.
//
// A flow is a path expressed as data: steps, a start, and where each step
// goes next. Most steps cost nothing to run -- collecting a value, branching
// on it, running a CLI -- and exactly one step type calls a model. That is
// the whole point. An assistant asked to conduct a five-question intake pays
// for five model turns and may still improvise its way off the script; the
// same intake as a flow costs zero tokens and cannot.
//
// The step set is closed on purpose, like bkn's query operators. A flow is
// not a programming language, and the moment it grows expressions it becomes
// one -- badly, and in a place nobody can debug.
package flow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Step types.
const (
	TypeSet     = "set"
	TypeMessage = "message"
	TypeCollect = "collect_input"
	TypeCond    = "conditional"
	TypeCmd     = "cmd"
	TypeHTTP    = "http"
	TypeScript  = "script"
	TypeAsk     = "ask"
	TypeEnd     = "end"
)

// Comparison operators. Closed set, no expressions.
const (
	OpEq       = "eq"
	OpNe       = "ne"
	OpContains = "contains"
	OpEmpty    = "empty"
	OpNotEmpty = "not_empty"
	OpGt       = "gt"
	OpLt       = "lt"
)

// Branch is one arm of a conditional: compare a variable, go somewhere.
type Branch struct {
	Var   string `json:"var"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
	Next  string `json:"next"`
}

// Step is one unit of work. Fields are shared across types rather than split
// into a union, because a flow is JSON a human writes by hand and a tagged
// union in JSON is unpleasant to write by hand.
type Step struct {
	Type string `json:"type"`
	Next string `json:"next,omitempty"`

	// set / collect_input / cmd / http / ask: where the result lands.
	Var string `json:"var,omitempty"`

	// set
	Value string `json:"value,omitempty"`

	// message / collect_input / ask
	Text     string `json:"text,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Required bool   `json:"required,omitempty"`

	// conditional
	When []Branch `json:"when,omitempty"`
	Else string   `json:"else,omitempty"`

	// cmd
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`

	// http
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`

	// cmd / http: how to read the output, and where to go when it fails.
	Parse string `json:"parse,omitempty"` // json | text (default text)
	// Select pulls one value out of parsed JSON by dotted path ("prs.0.number").
	// It is explicit rather than a dotted reference resolved at render time,
	// because a flow should say what it reads, once, where a reader can see it.
	Select  string `json:"select,omitempty"`
	OnError string `json:"on_error,omitempty"`

	// ask
	Assistant string `json:"assistant,omitempty"`

	// script names a bkn script to run. It carries the caller's own token, so
	// the script runs as the user and bkn's per-script run policy decides
	// whether it may run at all -- which is why this step exists at all and
	// why it needs no allowlist of its own: the gate lives in bkn, next to
	// the code being run.
	Script string `json:"script,omitempty"`
	// Input is a JSON object template sent as the script's input.
	Input string `json:"input,omitempty"`

	// end
	Status string `json:"status,omitempty"` // completed (default) | failed
}

// Flow is a whole definition.
type Flow struct {
	ID          string          `json:"id,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Start       string          `json:"start"`
	Steps       map[string]Step `json:"steps"`
	CreatedAt   string          `json:"created_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
}

// Parse reads a definition.
func Parse(raw []byte) (*Flow, error) {
	var f Flow
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("not a valid flow definition: %w", err)
	}
	return &f, nil
}

// Problem is one static finding. Fatal separates "this cannot run" from "this
// is probably a mistake", because a warning that blocks is a warning nobody
// reads and an error that does not block is a bug waiting to happen.
type Problem struct {
	Step    string `json:"step,omitempty"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

// Validate checks a flow without running it.
//
// This is the payoff of the path being data: unknown targets, unreachable
// steps, variables read before anything sets them, and commands that are not
// allowed can all be found before a run spends a cent. allowed is the local
// command allowlist; pass nil to skip that check.
func Validate(f *Flow, allowedCmds, allowedHosts []string) []Problem {
	var out []Problem
	add := func(step, msg string, fatal bool) {
		out = append(out, Problem{Step: step, Message: msg, Fatal: fatal})
	}
	if strings.TrimSpace(f.Name) == "" {
		add("", "flow needs a name", true)
	}
	if len(f.Steps) == 0 {
		add("", "flow has no steps", true)
		return out
	}
	if f.Start == "" {
		add("", "flow needs a start step", true)
	} else if _, ok := f.Steps[f.Start]; !ok {
		add("", "start step "+f.Start+" does not exist", true)
	}

	// Deterministic order, so two runs of the validator agree.
	names := make([]string, 0, len(f.Steps))
	for n := range f.Steps {
		names = append(names, n)
	}
	sort.Strings(names)

	targets := func(s Step) []string {
		t := []string{}
		if s.Next != "" {
			t = append(t, s.Next)
		}
		if s.Else != "" {
			t = append(t, s.Else)
		}
		if s.OnError != "" {
			t = append(t, s.OnError)
		}
		for _, b := range s.When {
			if b.Next != "" {
				t = append(t, b.Next)
			}
		}
		return t
	}

	for _, name := range names {
		s := f.Steps[name]
		validateStep(name, s, add, allowedCmds, allowedHosts)
		for _, t := range targets(s) {
			if _, ok := f.Steps[t]; !ok {
				add(name, "goes to "+t+", which does not exist", true)
			}
		}
	}

	// Reachability, from the start.
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, t := range targets(f.Steps[n]) {
			if _, ok := f.Steps[t]; ok {
				walk(t)
			}
		}
	}
	if _, ok := f.Steps[f.Start]; ok {
		walk(f.Start)
	}
	for _, name := range names {
		if !seen[name] {
			add(name, "is unreachable from the start step", false)
		}
	}

	// Variables read before anything could have set them. Approximate on
	// purpose: it walks in name order rather than following every path, so it
	// warns rather than blocks.
	set := map[string]bool{}
	for _, name := range names {
		s := f.Steps[name]
		if s.Var == "" {
			continue
		}
		set[s.Var] = true
		// A cmd or http step sets three variables, not one: the output, the
		// exit code and the error text. A flow that branches on the code is
		// doing the intended thing and must not be warned about it.
		if s.Type == TypeCmd || s.Type == TypeHTTP || s.Type == TypeScript {
			set[s.Var+"_code"] = true
			set[s.Var+"_err"] = true
		}
	}
	for _, name := range names {
		for _, ref := range Refs(f.Steps[name]) {
			if strings.HasPrefix(ref, "secret.") || strings.HasPrefix(ref, "input.") {
				continue
			}
			if !set[ref] {
				add(name, "reads {{"+ref+"}}, which no step sets", false)
			}
		}
	}
	return out
}

func validateStep(name string, s Step, add func(string, string, bool), allowedCmds, allowedHosts []string) {
	switch s.Type {
	case TypeSet:
		if s.Var == "" {
			add(name, "set needs a var", true)
		}
	case TypeMessage:
		if s.Text == "" {
			add(name, "message needs text", true)
		}
	case TypeCollect:
		if s.Var == "" {
			add(name, "collect_input needs a var", true)
		}
		if s.Prompt == "" {
			add(name, "collect_input needs a prompt, so a caller knows what to supply", true)
		}
	case TypeCond:
		if len(s.When) == 0 {
			add(name, "conditional needs at least one branch", true)
		}
		for _, b := range s.When {
			if b.Var == "" || b.Next == "" {
				add(name, "every branch needs a var and a next", true)
			}
			if !validOp(b.Op) {
				add(name, "unknown operator "+b.Op+"; use "+strings.Join(Ops(), ", "), true)
			}
		}
		if s.Else == "" {
			add(name, "conditional has no else; a run that matches nothing would stop here", false)
		}
	case TypeCmd:
		if s.Select != "" && s.Parse != "json" {
			add(name, "select needs parse: json", true)
		}
		if s.Command == "" {
			add(name, "cmd needs a command", true)
		} else if !contains(allowedCmds, s.Command) {
			// A warning, not a fatal: the allowlist is machine-local state,
			// and refusing to store a definition because THIS machine cannot
			// run it would make flows unshareable. The engine refuses the
			// step at run time regardless, which is the gate that matters.
			add(name, "command "+s.Command+" is not allowed on this machine "+
				"(ahub config set --allow-cmd "+s.Command+")", false)
		}
	case TypeHTTP:
		if s.Select != "" && s.Parse != "json" {
			add(name, "select needs parse: json", true)
		}
		if s.URL == "" {
			add(name, "http needs a url", true)
		} else {
			host := hostOf(s.URL)
			if host == "" {
				add(name, "url is templated, so its host cannot be checked ahead of time", false)
			} else if !contains(allowedHosts, host) {
				add(name, "host "+host+" is not allowed on this machine "+
					"(ahub config set --allow-host "+host+")", false)
			}
		}
	case TypeScript:
		if s.Script == "" {
			add(name, "script needs a script name", true)
		}
		if s.Select != "" && s.Parse != "json" {
			add(name, "select needs parse: json", true)
		}
	case TypeAsk:
		if s.Assistant == "" {
			add(name, "ask needs an assistant", true)
		}
		if s.Prompt == "" {
			add(name, "ask needs a prompt", true)
		}
	case TypeEnd:
		if s.Status != "" && s.Status != "completed" && s.Status != "failed" {
			add(name, "end status must be completed or failed", true)
		}
	case "":
		add(name, "step has no type", true)
	default:
		add(name, "unknown step type "+s.Type, true)
	}
	if s.Type != TypeEnd && s.Type != TypeCond && s.Next == "" {
		add(name, "has no next and is not an end step", true)
	}
}

// Ops lists the comparison operators.
func Ops() []string {
	return []string{OpEq, OpNe, OpContains, OpEmpty, OpNotEmpty, OpGt, OpLt}
}

func validOp(op string) bool { return contains(Ops(), op) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Fatal reports whether any problem blocks execution.
func Fatal(ps []Problem) bool {
	for _, p := range ps {
		if p.Fatal {
			return true
		}
	}
	return false
}
