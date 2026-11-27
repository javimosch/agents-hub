package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// relay records what the CLI sends to the feedback relay.
func relay(t *testing.T, status int) (url string, got chan map[string]any) {
	t.Helper()
	got = make(chan map[string]any, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{"_route": r.Method + " " + r.URL.Path}
		_ = json.Unmarshal(raw, &body)
		got <- body
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true,"stored":true}`))
	}))
	t.Cleanup(server.Close)
	return server.URL, got
}

// received is the submission the relay got. The CLI has exited by now, so
// there is nothing to wait for.
func received(t *testing.T, got chan map[string]any) map[string]any {
	t.Helper()
	select {
	case body := <-got:
		return body
	default:
		t.Fatal("nothing reached the relay")
		return nil
	}
}

func sendFeedback(t *testing.T, relayURL string, args ...string) (result, map[string]any) {
	t.Helper()
	env := append(cliEnv(t, "http://127.0.0.1:1"), "FEEDBACK_RELAY="+relayURL,
		"USER=a-private-username", "AHUB_TOKEN=")
	r := runCLI(t, os.Args[0], env, append([]string{"feedback"}, args...)...)
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal([]byte(r.stdout), &out)
	return r, out.Data
}

func TestFeedbackSendsOneSubmissionToTheRelay(t *testing.T) {
	// The spec spells the flags with one dash; ahub's use two.
	for _, dash := range []string{"-", "--"} {
		url, got := relay(t, 200)
		r, data := sendFeedback(t, url, "the lease never expires", dash+"kind", "bug", dash+"context", "schedules")
		if r.code != 0 || data["relayed"] != 1.0 || data["stored"] != 0.0 {
			t.Fatalf("%skind: got exit %d, data %v (%s)", dash, r.code, data, r.stderr)
		}
		sent := received(t, got)
		for k, want := range map[string]string{"_route": "POST /v1/feedback", "app": "ahub",
			"message": "the lease never expires", "kind": "bug", "context": "schedules"} {
			if sent[k] != want {
				t.Errorf("%s: got %v, want %q", k, sent[k], want)
			}
		}
		id, _ := sent["id"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || data["id"] != id {
			t.Errorf("id: sent %q, reported %v", id, data["id"])
		}
		if sent["version"] == nil {
			t.Error("no version was sent")
		}
	}
}

// The OS username is personal data nobody asked to send.
func TestFeedbackDoesNotSendTheUsername(t *testing.T) {
	url, got := relay(t, 200)
	sendFeedback(t, url, "hello")
	sent := received(t, got)
	if sent["reporter"] != "agent" || sent["kind"] != "note" {
		t.Errorf("got reporter %v kind %v, want agent and note", sent["reporter"], sent["kind"])
	}
	if raw, _ := json.Marshal(sent); strings.Contains(string(raw), "a-private-username") {
		t.Errorf("the username was sent: %s", raw)
	}
}

func TestFeedbackNeverFailsTheCaller(t *testing.T) {
	failing, _ := relay(t, 500)
	for name, url := range map[string]string{"relay error": failing, "unreachable": "http://127.0.0.1:1", "off": "off"} {
		t.Run(name, func(t *testing.T) {
			r, data := sendFeedback(t, url, "hello")
			if r.code != 0 || data["relayed"] != 0.0 {
				t.Errorf("got exit %d, data %v, want 0 with relayed 0", r.code, data)
			}
		})
	}
}

func TestFeedbackNeedsAMessage(t *testing.T) {
	url, got := relay(t, 200)
	r, _ := sendFeedback(t, url, "   ")
	wantError(t, r, 82, "validation_error")
	if len(got) != 0 {
		t.Error("an empty message was sent")
	}
}
