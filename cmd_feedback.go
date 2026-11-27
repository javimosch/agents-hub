package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/out"
)

// defaultRelay is the shared feedback relay of cli-feedback-spec.
const defaultRelay = "https://feedback.intrane.fr"

// feedbackCmd is cli-feedback-spec's relay-only variant: ahub has no server of
// its own. Nothing is sent unless somebody runs this, and what is sent is what
// they typed plus the tool's version.
func feedbackCmd(fl *flags) {
	kind, about, reporter := fl.str("kind"), fl.str("context"), fl.str("reporter")
	var words []string
	// The spec writes these flags with one dash.
	for i := 0; i < len(fl.rest); i++ {
		switch {
		case fl.rest[i] == "-kind" && i+1 < len(fl.rest):
			i++
			kind = fl.rest[i]
		case fl.rest[i] == "-context" && i+1 < len(fl.rest):
			i++
			about = fl.rest[i]
		default:
			words = append(words, fl.rest[i])
		}
	}
	message := strings.TrimSpace(strings.Join(words, " "))
	if message == "" {
		out.Fail(out.ValidationError, "validation_error", "give a message",
			`ahub feedback "<message>" --kind bug`)
	}
	if kind == "" {
		kind = "note"
	}
	if reporter == "" {
		// The spec suggests $USER. An OS username is personal data nobody
		// asked to send, so a name is only sent when one is given.
		reporter = "agent"
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	id := hex.EncodeToString(raw)
	relayed := 0
	if relayFeedback(map[string]string{
		"id": id, "app": "ahub", "version": Version, "kind": kind,
		"message": message, "context": about, "reporter": reporter,
	}) {
		relayed = 1
	}
	// A failed write is reported, never raised: feedback must not fail a run.
	out.Data(map[string]any{"data": map[string]any{"id": id, "stored": 0, "relayed": relayed}})
}

// relayFeedback makes one attempt. FEEDBACK_RELAY=off sends nothing.
func relayFeedback(body map[string]string) bool {
	base := os.Getenv("FEEDBACK_RELAY")
	if base == "off" {
		out.Log("feedback not sent: FEEDBACK_RELAY=off")
		return false
	}
	if base == "" {
		base = defaultRelay
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(strings.TrimSuffix(base, "/")+"/v1/feedback",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		out.Log("feedback not delivered: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		out.Log("feedback not delivered: the relay answered %d", resp.StatusCode)
		return false
	}
	return true
}
