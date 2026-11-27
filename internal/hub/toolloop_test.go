package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/javimosch/agents-hub/internal/provider"
)

// noopRegistry offers one tool and counts how often it actually ran.
func noopRegistry(ran *int) *Registry {
	var r *Registry
	return r.With("noop", Tool{
		Spec: provider.Tool{Name: "noop"},
		Run: func(context.Context, json.RawMessage) (string, error) {
			*ran++
			return "done", nil
		},
	})
}

func TestMaxStepsLeavesAResumableThread(t *testing.T) {
	h, _ := newTestHub(t)
	ran := 0
	p := &scripted{steps: []step{asksFor(1, "c1", "c2"), answer("resumed", 1)}}
	agent := Agent{Model: "m"}

	first, err := h.Chat(context.Background(), p, ChatOptions{
		Agent: agent, Message: "go", Tools: noopRegistry(&ran), MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Finish != "max_steps" || ran != 0 {
		t.Fatalf("finish=%q ran=%d, want max_steps and 0", first.Finish, ran)
	}

	th, err := h.GetThread(first.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if th.Messages != 4 { // user, assistant, two placeholder results
		t.Errorf("thread counts %d messages, want 4", th.Messages)
	}
	second, err := h.Chat(context.Background(), p, ChatOptions{Agent: agent, Thread: &th, Message: "again"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.Reply.Content != "resumed" {
		t.Fatalf("reply = %q", second.Reply.Content)
	}
}

func TestUnofferedToolCallLeavesAResumableThread(t *testing.T) {
	h, f := newTestHub(t)
	p := &scripted{steps: []step{asksFor(1, "c1"), answer("resumed", 1)}}
	agent := Agent{Model: "m"}
	if _, err := h.Chat(context.Background(), p, ChatOptions{Agent: agent, Message: "go"}); err == nil {
		t.Fatal("want an error when no tools are enabled")
	}
	th, err := h.GetThread(f.all(RefThreads)[0].Str("id"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Chat(context.Background(), p, ChatOptions{Agent: agent, Thread: &th, Message: "again"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
}
