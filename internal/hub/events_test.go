package hub

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

func recordEvents(into *[]string) func(Event) {
	return func(e Event) {
		switch e.Type {
		case "start":
			*into = append(*into, "start")
		case "tool_call":
			*into = append(*into, fmt.Sprintf("tool_call %d %s %s %s", e.Step, e.Name, e.ID, e.Args))
		case "tool_result":
			*into = append(*into, fmt.Sprintf("tool_result %d %s %s %d %q", e.Step, e.Name, e.ID, e.Chars, e.Failed))
		}
	}
}

func TestEventsReportEachToolCallAroundItsRun(t *testing.T) {
	h, _ := newTestHub(t)
	var got []string
	ran := 0
	tools := noopRegistry(&ran)
	opts := ChatOptions{Agent: Agent{Model: "m"}, Message: "go", Tools: tools,
		OnEvent: recordEvents(&got)}
	p := &scripted{steps: []step{asksFor(1, "c1", "c2"), answer("done", 1)}}
	if _, err := h.Chat(context.Background(), p, opts); err != nil {
		t.Fatal(err)
	}
	want := []string{"start",
		"tool_call 1 noop c1 {}", `tool_result 1 noop c1 4 ""`,
		"tool_call 1 noop c2 {}", `tool_result 1 noop c2 4 ""`}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\ngot  %q\nwant %q", got, want)
	}
}

// A turn that dies at the model must already have said which thread to resume.
func TestStartEventNamesTheThreadBeforeTheModelIsCalled(t *testing.T) {
	h, f := newTestHub(t)
	var thread string
	p := &scripted{steps: []step{{err: fmt.Errorf("provider down")}}}
	_, err := h.Chat(context.Background(), p, ChatOptions{Agent: Agent{Model: "m"}, Message: "go",
		OnEvent: func(e Event) {
			if e.Type == "start" {
				thread = e.Thread
				if n := len(f.all(RefMessages)); n != 1 {
					t.Errorf("start came with %d stored messages, want the question", n)
				}
			}
		}})
	if err == nil {
		t.Fatal("want the provider error")
	}
	if stored := f.all(RefThreads)[0].Str("id"); thread != stored {
		t.Fatalf("start named thread %q, want %q", thread, stored)
	}
}

func TestACallThatNeverRunsIsNotAnnounced(t *testing.T) {
	h, _ := newTestHub(t)
	var got []string
	ran := 0
	p := &scripted{steps: []step{asksFor(1, "c1")}}
	_, err := h.Chat(context.Background(), p, ChatOptions{Agent: Agent{Model: "m"}, Message: "go",
		Tools: noopRegistry(&ran), MaxSteps: 1, OnEvent: recordEvents(&got)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"start"}) {
		t.Fatalf("events = %q, want only start", got)
	}
}
