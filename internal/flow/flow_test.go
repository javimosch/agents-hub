package flow

import (
	"context"
	"strings"
	"testing"
)

func mustParse(t *testing.T, raw string) *Flow {
	t.Helper()
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

const intake = `{
  "name": "intake",
  "start": "name",
  "steps": {
    "name":  {"type":"collect_input","var":"who","prompt":"Your name?","required":true,"next":"tier"},
    "tier":  {"type":"conditional","when":[{"var":"who","op":"eq","value":"vip","next":"gold"}],"else":"std"},
    "gold":  {"type":"message","text":"Welcome back {{who}}","next":"done"},
    "std":   {"type":"message","text":"Hello {{who}}","next":"done"},
    "done":  {"type":"end"}
  }
}`

// The load-bearing claim of the whole feature: a wizard that collects and
// branches costs nothing. If this ever starts calling a model, the reason to
// have flows at all is gone.
func TestDeterministicRunCostsNothing(t *testing.T) {
	f := mustParse(t, intake)
	e := &Engine{Flow: f}
	run := NewRun(f, nil)
	e.Start(context.Background(), run)

	if run.Status != StatusPaused {
		t.Fatalf("want paused for input, got %s (%s)", run.Status, run.Error)
	}
	if run.Needs == nil || run.Needs.Var != "who" {
		t.Fatalf("wrong need: %+v", run.Needs)
	}
	e.Resume(context.Background(), run, map[string]string{"who": "vip"})
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s (%s)", run.Status, run.Error)
	}
	if run.LLMCalls != 0 || run.Tokens != 0 {
		t.Fatalf("a deterministic flow spent tokens: calls=%d tokens=%d", run.LLMCalls, run.Tokens)
	}
	if len(run.Messages) != 1 || !strings.Contains(run.Messages[0], "Welcome back vip") {
		t.Fatalf("wrong branch taken: %v", run.Messages)
	}
}

// A required value that arrives as an input must not pause at all.
func TestSuppliedInputSkipsThePause(t *testing.T) {
	f := mustParse(t, intake)
	e := &Engine{Flow: f}
	run := NewRun(f, map[string]string{"who": "ada"})
	e.Start(context.Background(), run)
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s", run.Status)
	}
	if run.Messages[0] != "Hello ada" {
		t.Fatalf("wrong message: %q", run.Messages[0])
	}
}

// A cycle must stop at the step cap rather than run forever.
func TestCycleStopsAtTheCap(t *testing.T) {
	f := mustParse(t, `{"name":"loop","start":"a","steps":{
		"a":{"type":"set","var":"x","value":"1","next":"b"},
		"b":{"type":"set","var":"y","value":"2","next":"a"}}}`)
	e := &Engine{Flow: f, MaxSteps: 10}
	run := NewRun(f, nil)
	e.Start(context.Background(), run)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "may cycle") {
		t.Fatalf("cycle not caught: %s / %s", run.Status, run.Error)
	}
}

// An unset variable fails the step instead of substituting an empty string:
// a command with a silently empty argument is a different command.
func TestUnsetVariableFails(t *testing.T) {
	f := mustParse(t, `{"name":"u","start":"a","steps":{
		"a":{"type":"message","text":"hi {{nobody}}","next":"b"},
		"b":{"type":"end"}}}`)
	e := &Engine{Flow: f}
	run := NewRun(f, nil)
	e.Start(context.Background(), run)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "nobody") {
		t.Fatalf("unset variable not caught: %s / %s", run.Status, run.Error)
	}
}

func TestValidateCatchesWhatItShould(t *testing.T) {
	f := mustParse(t, `{"name":"bad","start":"a","steps":{
		"a":{"type":"cmd","command":"rm","args":["-rf","/"],"var":"o","next":"ghost"},
		"orphan":{"type":"message","text":"{{never_set}}","next":"a"}}}`)
	ps := Validate(f, []string{"echo"}, nil)
	var got []string
	for _, p := range ps {
		got = append(got, p.Message)
	}
	joined := strings.Join(got, " | ")
	for _, want := range []string{"does not exist", "is not allowed on this machine", "unreachable", "which no step sets"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("validator missed %q; got: %s", want, joined)
		}
	}
	if !Fatal(ps) {
		t.Fatalf("a flow pointing at a missing step should be fatal")
	}
}

// The allowlist is a warning at validation time and a refusal at run time:
// a definition must stay storable on a machine that cannot run it, and must
// never actually run there.
func TestAllowlistWarnsButDoesNotBlockStoring(t *testing.T) {
	f := mustParse(t, `{"name":"c","start":"a","steps":{
		"a":{"type":"cmd","command":"gh","args":["pr","list"],"var":"o","next":"b"},
		"b":{"type":"end"}}}`)
	ps := Validate(f, nil, nil)
	if Fatal(ps) {
		t.Fatalf("an unlisted command should not block storing: %+v", ps)
	}
	found := false
	for _, p := range ps {
		if strings.Contains(p.Message, "not allowed on this machine") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning about the unlisted command: %+v", ps)
	}
}

// The variables a cmd step derives are real variables, and reading one is not
// a mistake to warn about.
func TestDerivedVariablesAreKnown(t *testing.T) {
	f := mustParse(t, `{"name":"d","start":"a","steps":{
		"a":{"type":"cmd","command":"git","args":["status"],"var":"g","next":"b","on_error":"b"},
		"b":{"type":"message","text":"code {{g_code}} err {{g_err}}","next":"c"},
		"c":{"type":"end"}}}`)
	for _, p := range Validate(f, []string{"git"}, nil) {
		if strings.Contains(p.Message, "which no step sets") {
			t.Fatalf("derived variable reported as unset: %s", p.Message)
		}
	}
}

// A command that is not on the allowlist never runs, whatever the flow says.
func TestCmdRefusesUnlistedCommand(t *testing.T) {
	_, err := RunCmd(context.Background(), Limits{Commands: []string{"echo"}}, "rm", []string{"-rf", "/"}, "")
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unlisted command was not refused: %v", err)
	}
}

// Arguments are passed as argv, so a value containing shell syntax stays one
// argument and means nothing to a shell that is never involved.
func TestCmdPassesArgvNotAShellLine(t *testing.T) {
	out, err := RunCmd(context.Background(), Limits{Commands: []string{"echo"}},
		"echo", []string{"a; rm -rf / && echo b"}, "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(out.Text) != "a; rm -rf / && echo b" {
		t.Fatalf("argument was reinterpreted: %q", out.Text)
	}
}

// A secret may be referenced by name but must never reach a stored run.
func TestSecretsAreRedacted(t *testing.T) {
	r := Renderer{Data: map[string]string{}, Secrets: map[string]string{"TOKEN": "hunter2"}}
	got, err := r.Render("Bearer {{secret.TOKEN}}")
	if err != nil || got != "Bearer hunter2" {
		t.Fatalf("render: %q %v", got, err)
	}
	if red := r.Redact("failed with Bearer hunter2"); strings.Contains(red, "hunter2") {
		t.Fatalf("secret survived redaction: %q", red)
	}
}

// Numeric comparison reads the leading number, which is what lets the output
// of `du -sb` ("1157660672\t/var/log/journal") be compared directly against a
// budget. This is relied on by any flow that measures something, so it is
// pinned here rather than left as an accident of the parser.
func TestNumericCompareReadsLeadingNumber(t *testing.T) {
	f := mustParse(t, `{"name":"n","start":"c","steps":{
		"c":{"type":"conditional","when":[{"var":"used","op":"gt","value":"1000"}],"else":"small"},
		"big":{"type":"message","text":"over","next":"end"},
		"small":{"type":"message","text":"under","next":"end"},
		"end":{"type":"end"}}}`)
	f.Steps["c"].When[0].Next = "big"
	c := f.Steps["c"]
	c.When[0].Next = "big"
	f.Steps["c"] = c

	e := &Engine{Flow: f}
	run := NewRun(f, map[string]string{"used": "1157660672\t/var/log/journal"})
	e.Start(context.Background(), run)
	if run.Status != StatusCompleted || run.Messages[0] != "over" {
		t.Fatalf("du output did not compare numerically: %s / %v", run.Status, run.Messages)
	}

	run = NewRun(f, map[string]string{"used": "12\t/tmp/small"})
	e.Start(context.Background(), run)
	if run.Messages[0] != "under" {
		t.Fatalf("small value compared wrong: %v", run.Messages)
	}
}

// A conditional that branches on a variable nothing sets would compare against
// the empty string forever, so the validator says so.
func TestValidateWarnsOnUnsetBranchVariable(t *testing.T) {
	f := mustParse(t, `{"name":"n","start":"c","steps":{
		"c":{"type":"conditional","when":[{"var":"moed","op":"eq","value":"enforce","next":"a"}],"else":"a"},
		"a":{"type":"end"}}}`)
	found := false
	for _, p := range Validate(f, nil, nil) {
		if strings.Contains(p.Message, "branches on moed") {
			found = true
		}
	}
	if !found {
		t.Fatal("a typo in a conditional variable went unreported")
	}
}
