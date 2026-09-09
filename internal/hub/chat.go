package hub

import (
	"context"
	"fmt"

	"github.com/javimosch/agents-hub/internal/provider"
)

// Turn is everything one exchange produced.
type Turn struct {
	Thread    Thread         `json:"thread"`
	User      Message        `json:"user_message"`
	Assistant Message        `json:"assistant_message"`
	Model     string         `json:"model"`
	Usage     provider.Usage `json:"usage"`
	Finish    string         `json:"finish_reason,omitempty"`
}

// ChatOptions is one call to Chat.
type ChatOptions struct {
	Assistant Assistant
	Thread    *Thread // nil opens a new one
	Message   string
	// HistoryLimit caps how many prior messages are replayed to the model. It
	// is a blunt window, not compaction: a real compaction strategy belongs in
	// the agentic engine, and pretending a truncated head is a summary would
	// hide the loss instead of bounding it.
	HistoryLimit int
	OnDelta      func(string)
}

// Chat runs one exchange: replay the thread, ask the model, persist both turns.
//
// The user message is stored BEFORE the model is called. A provider timeout
// then costs the answer but not the question -- the thread stays a faithful
// record of what was asked, and the turn can be retried without the agent
// having to remember what it said.
func (h *Hub) Chat(ctx context.Context, p provider.Provider, opts ChatOptions) (*Turn, error) {
	if opts.Message == "" {
		return nil, fmt.Errorf("message is required")
	}

	th := opts.Thread
	if th == nil {
		created, err := h.CreateThread(opts.Assistant, titleFrom(opts.Message))
		if err != nil {
			return nil, err
		}
		th = &created
	}

	history, err := h.History(th.ID, opts.HistoryLimit)
	if err != nil {
		return nil, err
	}

	msgs := make([]provider.Message, 0, len(history)+2)
	if opts.Assistant.System != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: opts.Assistant.System})
	}
	for _, m := range history {
		msgs = append(msgs, provider.Message{Role: m.Role, Content: m.Content})
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: opts.Message})

	seq := nextSeq(history)
	userMsg, err := h.AppendMessage(Message{
		ThreadID: th.ID, Role: provider.RoleUser, Content: opts.Message, Seq: seq,
	})
	if err != nil {
		return nil, err
	}
	// The counter is advanced with the message rather than at the end of the
	// turn. A provider failure then leaves a thread that says four messages
	// and holds four messages; deferring both updates to the end would leave
	// it claiming three.
	if err := h.touch(th, seq+1); err != nil {
		return nil, err
	}

	resp, err := p.Chat(ctx, provider.Request{
		Model:       opts.Assistant.Model,
		Messages:    msgs,
		Temperature: opts.Assistant.Temperature,
		MaxTokens:   opts.Assistant.MaxTokens,
	}, opts.OnDelta)
	if err != nil {
		return nil, err
	}

	model := resp.Model
	if model == "" {
		model = opts.Assistant.Model
	}
	asstMsg, err := h.AppendMessage(Message{
		ThreadID: th.ID, Role: provider.RoleAssistant, Content: resp.Content,
		Model: model, Seq: seq + 1,
		Prompt: resp.Usage.PromptTokens, Completed: resp.Usage.CompletionTokens,
	})
	if err != nil {
		return nil, err
	}

	if err := h.touch(th, seq+2); err != nil {
		return nil, err
	}

	return &Turn{
		Thread: *th, User: userMsg, Assistant: asstMsg,
		Model: model, Usage: resp.Usage, Finish: resp.FinishReason,
	}, nil
}

// touch records how many messages the thread now holds and when it last moved.
func (h *Hub) touch(th *Thread, count int) error {
	th.Messages = count
	_, err := h.C.Patch(RefThreads, th.ID, map[string]any{
		"messages": count, "updated_at": now(),
	})
	return err
}

// nextSeq continues the thread's numbering. It reads the last stored seq
// rather than counting messages, so a deleted message in the middle does not
// make the next turn collide with an existing one.
func nextSeq(history []Message) int {
	max := -1
	for _, m := range history {
		if m.Seq > max {
			max = m.Seq
		}
	}
	return max + 1
}
