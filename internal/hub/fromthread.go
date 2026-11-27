package hub

import (
	"encoding/json"
	"errors"

	"github.com/javimosch/agents-hub/internal/eval"
	"github.com/javimosch/agents-hub/internal/provider"
)

// ThreadRecord is what a stored thread did with its first question, in the
// shape an eval case needs.
type ThreadRecord struct {
	Question string
	// Questions counts every question asked. A case sends one message, so
	// only the first exchange can be replayed.
	Questions int
	Outcome   eval.Outcome
}

// ReadThread reads a thread's first exchange. The outcome is what the thread's
// assertions would have seen, so they can be tried on it before they are saved.
func ReadThread(msgs []Message) (ThreadRecord, error) {
	var r ThreadRecord
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			r.Questions++
			if r.Questions == 1 {
				r.Question = m.Content
			}
			continue
		}
		if r.Questions != 1 || m.Role != provider.RoleAssistant {
			continue
		}
		r.Outcome.Answer = m.Content
		r.Outcome.Steps++
		r.Outcome.Tokens += m.Prompt + m.Completed
		if m.ToolCalls == "" {
			continue
		}
		var calls []provider.ToolCall
		if err := json.Unmarshal([]byte(m.ToolCalls), &calls); err != nil {
			return r, err
		}
		for _, c := range calls {
			r.Outcome.Tools = append(r.Outcome.Tools, c.Name)
		}
	}
	if r.Question == "" {
		return r, errors.New("the thread has no question to replay")
	}
	return r, nil
}
