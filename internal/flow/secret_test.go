package flow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// awkward has characters that each encoder writes differently.
const awkward = `Pl4nted "q" <&> é/+ \z`

// echo answers like a careless API: it repeats the request, escaping as the
// strictest common encoders do.
func echo(t *testing.T, status int, got *[]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if got != nil {
			*got = append(*got, string(b))
		}
		var req map[string]string
		_ = json.Unmarshal(b, &req)
		w.WriteHeader(status)
		// Go's default escapes <, > and &; é is what an ASCII-only encoder sends.
		out, _ := json.Marshal(map[string]any{"got": req, "raw": req["pw"], "id": "acct_1"})
		_, _ = w.Write([]byte(strings.ReplaceAll(string(out), "é", `é`)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func secretFlow(url string, after map[string]Step) *Flow {
	steps := map[string]Step{
		"pw": {Type: TypeCollect, Var: "pw", Prompt: "Password?", Required: true, Secret: true, Next: "send"},
		"send": {Type: TypeHTTP, Method: "POST", URL: url, Body: `{"pw":"{{pw}}"}`, Var: "reply",
			Next: "end", OnError: "refused"},
		"refused": {Type: TypeMessage, Text: "refused: {{reply}}", Next: "bad"},
		"bad":     {Type: TypeEnd, Status: StatusFailed},
		"end":     {Type: TypeEnd},
	}
	for k, v := range after {
		steps[k] = v
	}
	return &Flow{Name: "f", Start: "pw", Steps: steps}
}

var localhost = Limits{Hosts: []string{"127.0.0.1"}, Commands: []string{"cat", "echo"}}

// mentions reports whether any form of the secret is anywhere in the run.
func mentions(t *testing.T, run *Run) bool {
	t.Helper()
	b, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	// Decode so that no encoder's escaping can hide it, then search each string.
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any) bool
	walk = func(x any) bool {
		switch s := x.(type) {
		case string:
			var inner any
			if json.Unmarshal([]byte(s), &inner) == nil {
				if _, again := inner.(string); !again && walk(inner) {
					return true
				}
			}
			return strings.Contains(s, "Pl4nted")
		case map[string]any:
			for k, c := range s {
				if walk(k) || walk(c) {
					return true
				}
			}
		case []any:
			for _, c := range s {
				if walk(c) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

func TestASecretInputReachesTheServiceAndNothingElse(t *testing.T) {
	for name, status := range map[string]int{"accepted": 201, "refused with an echo": 422} {
		t.Run(name, func(t *testing.T) {
			var got []string
			f := secretFlow(echo(t, status, &got), nil)
			run := NewRun(f, map[string]string{"pw": awkward})
			(&Engine{Flow: f, Limits: localhost}).Start(context.Background(), run)
			var sent map[string]string
			if len(got) != 1 || json.Unmarshal([]byte(got[0]), &sent) != nil || sent["pw"] != awkward {
				t.Fatalf("the service got %q (%s)", got, run.Error)
			}
			if mentions(t, run) {
				t.Errorf("the run still holds it: %+v", run)
			}
			if _, kept := run.Data["pw"]; kept {
				t.Error("the value is still in the run's data")
			}
		})
	}
}

// A later step must not be able to pass an echoed secret on.
func TestAnEchoIsRemovedBeforeTheNextStepSeesIt(t *testing.T) {
	var asked string
	f := secretFlow(echo(t, 200, nil), map[string]Step{
		"send": {Type: TypeHTTP, Method: "POST", URL: "", Body: `{"pw":"{{pw}}"}`, Var: "reply", Next: "ask"},
		"ask":  {Type: TypeAsk, Agent: "a", Prompt: "summarise {{reply}}", Next: "end"},
	})
	s := f.Steps["send"]
	s.URL = echo(t, 200, nil)
	f.Steps["send"] = s
	e := &Engine{Flow: f, Limits: localhost,
		Ask: func(_ context.Context, _, prompt, _ string) (string, string, int, error) {
			asked = prompt
			return "ok", "t", 1, nil
		}}
	run := NewRun(f, map[string]string{"pw": awkward})
	e.Start(context.Background(), run)
	if asked == "" {
		t.Fatalf("the model was not asked (%s)", run.Error)
	}
	if strings.Contains(asked, "Pl4nted") {
		t.Errorf("the model was sent the secret: %s", asked)
	}
}

func TestASecretIsAskedForAgainAfterAnotherPause(t *testing.T) {
	var got []string
	url := echo(t, 200, &got)
	f := secretFlow(url, map[string]Step{
		"pw":    {Type: TypeCollect, Var: "pw", Prompt: "Password?", Required: true, Secret: true, Next: "terms"},
		"terms": {Type: TypeCollect, Var: "terms", Prompt: "Accept?", Required: true, Next: "send"},
	})
	e := &Engine{Flow: f, Limits: localhost}
	run := NewRun(f, nil)
	e.Start(context.Background(), run)
	if run.Needs == nil || !run.Needs.Secret || run.Needs.Var != "pw" {
		t.Fatalf("needs = %+v, want the secret pw", run.Needs)
	}
	e.Resume(context.Background(), run, map[string]string{"pw": awkward})
	if run.Needs == nil || run.Needs.Var != "terms" || run.Needs.Secret || mentions(t, run) {
		t.Fatalf("needs = %+v, and the pause must not hold the secret", run.Needs)
	}
	e.Resume(context.Background(), run, map[string]string{"terms": "yes"})
	if run.Status != StatusPaused || run.Needs == nil || run.Needs.Var != "pw" || len(got) != 0 {
		t.Fatalf("status %s needs %+v sent %d; want pw asked again before sending", run.Status, run.Needs, len(got))
	}
	e.Resume(context.Background(), run, map[string]string{"pw": awkward})
	if run.Status != StatusCompleted || len(got) != 1 {
		t.Fatalf("status %s (%s), sent %d", run.Status, run.Error, len(got))
	}
}

func TestASecretMayOnlyGoWhereItIsNotKeptOrShown(t *testing.T) {
	refused := map[string]Step{
		"copied":                   {Type: TypeSet, Var: "x", Value: "{{pw}}", Next: "end"},
		"shown":                    {Type: TypeMessage, Text: "{{pw}}", Next: "end"},
		"sent to a model":          {Type: TypeAsk, Agent: "a", Prompt: "{{pw}}", Next: "end"},
		"sent to a script":         {Type: TypeScript, Script: "s", Input: `{"p":"{{pw}}"}`, Next: "end"},
		"in a url":                 {Type: TypeHTTP, URL: "http://127.0.0.1/{{pw}}", Next: "end"},
		"as an argument":           {Type: TypeCmd, Command: "echo", Args: []string{"{{pw}}"}, Next: "end"},
		"as a directory":           {Type: TypeCmd, Command: "echo", Dir: "{{pw}}", Next: "end"},
		"in a prompt":              {Type: TypeCollect, Var: "y", Prompt: "{{pw}}?", Next: "end"},
		"overwritten":              {Type: TypeSet, Var: "pw", Value: "v", Next: "end"},
		"a step's result":          {Type: TypeCmd, Command: "echo", Var: "pw", Next: "end"},
		"marked on the wrong step": {Type: TypeMessage, Text: "hi", Secret: true, Next: "end"},
	}
	for name, step := range refused {
		t.Run(name, func(t *testing.T) {
			f := secretFlow("http://127.0.0.1:1", map[string]Step{
				"pw":  {Type: TypeCollect, Var: "pw", Prompt: "Password?", Required: true, Secret: true, Next: "use"},
				"use": step,
			})
			if !Fatal(Validate(f, localhost.Commands, localhost.Hosts)) {
				t.Error("validate accepted it")
			}
			// A stored definition is data: the engine checks again.
			run := NewRun(f, map[string]string{"pw": awkward})
			(&Engine{Flow: f, Limits: localhost}).Start(context.Background(), run)
			if run.Status != StatusFailed || mentions(t, run) {
				t.Errorf("status %s, mentions %v; want a failed run without it", run.Status, mentions(t, run))
			}
		})
	}
}

func TestASecretMayGoInAHeaderACommandsStdinOrACondition(t *testing.T) {
	f := secretFlow(echo(t, 200, nil), map[string]Step{
		"pw":   {Type: TypeCollect, Var: "pw", Prompt: "Password?", Required: true, Secret: true, Next: "len"},
		"len":  {Type: TypeCond, When: []Branch{{Var: "pw", Op: OpEmpty, Next: "bad"}}, Else: "feed"},
		"feed": {Type: TypeCmd, Command: "cat", Stdin: "{{pw}}", Var: "out", Next: "hdr"},
		"hdr":  {Type: TypeHTTP, URL: echo(t, 200, nil), Headers: map[string]string{"X-K": "{{pw}}"}, Next: "end"},
	})
	if ps := Validate(f, localhost.Commands, localhost.Hosts); Fatal(ps) {
		t.Fatalf("validate refused it: %+v", ps)
	}
	run := NewRun(f, map[string]string{"pw": "Pl4nted-plain"})
	(&Engine{Flow: f, Limits: localhost}).Start(context.Background(), run)
	if run.Status != StatusCompleted || mentions(t, run) {
		t.Errorf("status %s (%s), mentions %v", run.Status, run.Error, mentions(t, run))
	}
}
