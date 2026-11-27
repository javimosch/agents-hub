package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const runMainEnv = "AHUB_TEST_RUN_MAIN"

// TestMain lets a test run the real CLI as a process: failures exit, so what
// a caller sees can only be observed from outside.
func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		if v := os.Getenv("AHUB_TEST_VERSION"); v != "" {
			Version = v
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type cliError struct {
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions"`
}

// runFailing runs ahub against an empty backend and returns its typed error.
func runFailing(t *testing.T, args ...string) cliError {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Count(r.URL.Path, "/") > 4 { // /v1/store/ns/coll/id
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"not_found","message":"no record"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	t.Cleanup(backend.Close)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = cliEnv(t, backend.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) {
		t.Fatalf("ahub %v: got err %v, want a failing exit", args, err)
	}
	var body struct {
		Error cliError `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &body); err != nil {
		t.Fatalf("ahub %v: stderr is not a typed error: %s", args, stderr.String())
	}
	return body.Error
}

func TestScheduleErrorsTalkAboutSchedules(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantMessage string
		wantSuggest string
	}{
		{"unknown subcommand", []string{"schedule", "bogus"}, "schedule takes ", ""},
		{"no name given", []string{"schedule", "show"}, "name a schedule", "ahub schedule list"},
		{"no such schedule", []string{"schedule", "show", "ghost"}, "no schedule named ghost", "ahub schedule list"},
		{"invalid create", []string{"schedule", "create"}, "name is required", "ahub schedule create "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runFailing(t, c.args...)
			if !strings.Contains(got.Message, c.wantMessage) {
				t.Errorf("message: got %q, want it to contain %q", got.Message, c.wantMessage)
			}
			if c.wantSuggest != "" && !slices.ContainsFunc(got.Suggestions, func(s string) bool {
				return strings.HasPrefix(s, c.wantSuggest)
			}) {
				t.Errorf("suggestions: got %q, want one starting %q", got.Suggestions, c.wantSuggest)
			}
		})
	}
}

func TestFailedRunsAreCountedAsSchedules(t *testing.T) {
	for n, want := range map[int]string{1: "1 schedule", 2: "2 schedules"} {
		if got := pluralSchedules(n); got != want {
			t.Errorf("pluralSchedules(%d): got %q, want %q", n, got, want)
		}
	}
}
