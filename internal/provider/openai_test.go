package provider

import (
	"strings"
	"testing"
)

// A provider that keeps only the first system message must still see the
// second one's content -- that is the failure this merge exists to prevent,
// and it is invisible at runtime because nothing errors.
func TestMergeSystemCollapsesLeadingRun(t *testing.T) {
	got := mergeSystem([]Message{
		{Role: RoleSystem, Content: "you are helper"},
		{Role: RoleSystem, Content: "recalled memory"},
		{Role: RoleUser, Content: "question"},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
	if got[0].Role != RoleSystem || got[0].Content != "you are helper\n\nrecalled memory" {
		t.Fatalf("system messages not merged: %q", got[0].Content)
	}
	if got[1].Role != RoleUser {
		t.Fatalf("user message lost: %+v", got[1])
	}
}

// A system message that appears later in a conversation is not part of the
// leading run and must be left where it is.
func TestMergeSystemLeavesLaterSystemAlone(t *testing.T) {
	in := []Message{
		{Role: RoleSystem, Content: "a"},
		{Role: RoleUser, Content: "q"},
		{Role: RoleSystem, Content: "b"},
	}
	got := mergeSystem(in)
	if len(got) != 3 || got[2].Content != "b" {
		t.Fatalf("later system message moved: %+v", got)
	}
}

func TestMergeSystemSingleIsUntouched(t *testing.T) {
	in := []Message{{Role: RoleSystem, Content: "a"}, {Role: RoleUser, Content: "q"}}
	if got := mergeSystem(in); len(got) != 2 || got[0].Content != "a" {
		t.Fatalf("single system message rewritten: %+v", got)
	}
}

// A streamed tool call arrives as fragments: the name in one event, the
// arguments a few characters at a time across several more, and two calls
// interleaved by index. Accumulating them wrongly produces valid-looking JSON
// that is missing half its arguments.
func TestStreamAccumulatesToolCallFragments(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"memory_recall","arguments":"{\"que"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"skill_show","arguments":"{\"na"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ry\":\"ports\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"me\":\"demo\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9}}`,
		"data: [DONE]",
	}, "\n\n")

	o := &openAI{}
	got, err := o.readStream(strings.NewReader(sse), func(string) {})
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d: %+v", len(got.ToolCalls), got.ToolCalls)
	}
	if got.ToolCalls[0].Name != "memory_recall" || got.ToolCalls[0].Arguments != `{"query":"ports"}` {
		t.Fatalf("call 0 wrong: %+v", got.ToolCalls[0])
	}
	if got.ToolCalls[1].Name != "skill_show" || got.ToolCalls[1].Arguments != `{"name":"demo"}` {
		t.Fatalf("call 1 wrong: %+v", got.ToolCalls[1])
	}
	if got.FinishReason != "tool_calls" {
		t.Fatalf("finish reason lost: %q", got.FinishReason)
	}
}

// A tool result must go out with tool_call_id and without a tool_calls array,
// or the provider rejects the whole conversation.
func TestWireMessagesShapesToolTurns(t *testing.T) {
	got := wireMessages([]Message{
		{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "f", Arguments: "{}"}}},
		{Role: RoleTool, Content: "result", ToolCallID: "c1", Name: "f"},
	})
	if _, ok := got[0]["tool_calls"]; !ok {
		t.Fatalf("assistant turn lost its tool_calls: %+v", got[0])
	}
	if _, ok := got[0]["content"]; !ok {
		t.Fatalf("content key must be present even when empty: %+v", got[0])
	}
	if got[1]["tool_call_id"] != "c1" {
		t.Fatalf("tool turn lost its id: %+v", got[1])
	}
	if _, ok := got[1]["tool_calls"]; ok {
		t.Fatalf("tool turn must not carry tool_calls: %+v", got[1])
	}
}
