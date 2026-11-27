package hub

import (
	"context"
	"fmt"

	"github.com/javimosch/agents-hub/internal/provider"
)

// step is one scripted provider answer.
type step struct {
	resp provider.Response
	err  error
}

// scripted replays steps in order and records what it was sent. Like a real
// OpenAI-compatible API, and unlike a lenient mock, it rejects a conversation
// whose tool calls and results do not pair up.
type scripted struct {
	steps []step
	seen  [][]provider.Message
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Chat(_ context.Context, req provider.Request, _ func(string)) (*provider.Response, error) {
	s.seen = append(s.seen, append([]provider.Message(nil), req.Messages...))
	if err := checkPairing(req.Messages); err != nil {
		return nil, err
	}
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("scripted provider: no step left")
	}
	next := s.steps[0]
	s.steps = s.steps[1:]
	if next.err != nil {
		return nil, next.err
	}
	return &next.resp, nil
}

func checkPairing(msgs []provider.Message) error {
	open := map[string]bool{}
	for i, m := range msgs {
		if m.Role == provider.RoleTool {
			if !open[m.ToolCallID] {
				return fmt.Errorf("400: message %d is a tool result with no matching call", i)
			}
			delete(open, m.ToolCallID)
			continue
		}
		if len(open) > 0 {
			return fmt.Errorf("400: message %d follows unanswered tool calls", i)
		}
		for _, c := range m.ToolCalls {
			open[c.ID] = true
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("400: conversation ends with unanswered tool calls")
	}
	return nil
}

func answer(text string, tokens int) step {
	return step{resp: provider.Response{Content: text, FinishReason: "stop",
		Usage: provider.Usage{TotalTokens: tokens}}}
}

func asksFor(tokens int, callIDs ...string) step {
	r := provider.Response{FinishReason: "tool_calls", Usage: provider.Usage{TotalTokens: tokens}}
	for _, id := range callIDs {
		r.ToolCalls = append(r.ToolCalls, provider.ToolCall{ID: id, Name: "noop", Arguments: "{}"})
	}
	return step{resp: r}
}
