package hub

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// Thread is one conversation with one agent.
type Thread struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	Agent    string `json:"agent"`
	Title    string `json:"title,omitempty"`
	Messages int    `json:"messages"`
	// Summary replaces every message up to SummarySeq when the thread is
	// replayed. The messages themselves are never deleted: what was said is a
	// record, and a summary that quietly destroyed its source would make a
	// conversation impossible to audit afterwards.
	Summary    string `json:"summary,omitempty"`
	SummarySeq int    `json:"summary_seq,omitempty"`
	// LastPrompt is the prompt_tokens the provider reported for the most
	// recent turn -- a measured number rather than an estimate, which is what
	// makes the compaction threshold trustworthy.
	LastPrompt int `json:"last_prompt_tokens,omitempty"`
	// CompactPrompt is what LastPrompt was when this thread was last folded.
	// If the prompt has not shrunk since, folding again would summarise a
	// little more and change nothing -- the size is coming from somewhere a
	// summary cannot reach, like the system prompt or a recalled block.
	CompactPrompt int    `json:"compacted_at_prompt,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// Message is one stored turn. Content is kept verbatim; the model and usage
// are recorded per message rather than per thread because an agent's model
// can change between turns and the cost accounting must survive that.
type Message struct {
	ID        string `json:"id"`
	ThreadID  string `json:"thread_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Model     string `json:"model,omitempty"`
	Seq       int    `json:"seq"`
	Prompt    int    `json:"prompt_tokens,omitempty"`
	Completed int    `json:"completion_tokens,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`

	// ToolCalls is the raw JSON array an agent turn asked for, stored as
	// a string rather than a nested object: the store would normalise the
	// object, and what has to survive here is exactly the bytes the provider
	// will be shown again when this thread is resumed.
	ToolCalls  string `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

func threadFrom(r bkn.Record) Thread {
	return Thread{
		ID:            r.Str("id"),
		AgentID:       r.Str("agent_id"),
		Agent:         r.Str("agent"),
		Title:         r.Str("title"),
		Messages:      int(r.Num("messages")),
		Summary:       r.Str("summary"),
		SummarySeq:    int(r.Num("summary_seq")),
		LastPrompt:    int(r.Num("last_prompt_tokens")),
		CompactPrompt: int(r.Num("compacted_at_prompt")),
		CreatedAt:     r.Str("created_at"),
		UpdatedAt:     r.Str("updated_at"),
	}
}

func messageFrom(r bkn.Record) Message {
	return Message{
		ID:        r.Str("id"),
		ThreadID:  r.Str("thread_id"),
		Role:      r.Str("role"),
		Content:   r.Str("content"),
		Model:     r.Str("model"),
		Seq:       int(r.Num("seq")),
		Prompt:    int(r.Num("prompt_tokens")),
		Completed: int(r.Num("completion_tokens")),
		CreatedAt: r.Str("created_at"),

		ToolCalls:  r.Str("tool_calls"),
		ToolCallID: r.Str("tool_call_id"),
		Name:       r.Str("name"),
	}
}

// CreateThread opens a conversation against an agent.
func (h *Hub) CreateThread(a Agent, title string) (Thread, error) {
	doc := map[string]any{
		"agent_id":   a.ID,
		"agent":      a.Name,
		"messages":   0,
		"created_at": now(),
		"updated_at": now(),
	}
	if title != "" {
		doc["title"] = title
	}
	rec, err := h.C.Create(RefThreads, doc)
	if err != nil {
		return Thread{}, err
	}
	return threadFrom(rec), nil
}

// ListThreads returns the caller's threads, most recently updated first.
func (h *Hub) ListThreads(agent string, limit int) ([]Thread, error) {
	q := url.Values{}
	q.Set("order_by", "updated_at")
	if agent != "" {
		q.Set("agent", agent)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefThreads, q)
	if err != nil {
		return nil, err
	}
	out := make([]Thread, 0, len(recs))
	for _, r := range recs {
		out = append(out, threadFrom(r))
	}
	return out, nil
}

// GetThread reads one thread by id.
func (h *Hub) GetThread(id string) (Thread, error) {
	rec, err := h.C.Get(RefThreads, id)
	if err != nil {
		return Thread{}, err
	}
	return threadFrom(rec), nil
}

// HistoryAfter returns the messages said after a given seq, which is how a
// compacted thread replays only the part its summary does not cover.
func (h *Hub) HistoryAfter(threadID string, afterSeq, limit int) ([]Message, error) {
	all, err := h.History(threadID, 0)
	if err != nil {
		return nil, err
	}
	// afterSeq 0 means nothing has been folded. Filtering on seq > 0 there
	// would silently drop the very first message of every thread, which is
	// the user's opening question -- the one message a conversation can least
	// afford to lose.
	if afterSeq <= 0 {
		if limit > 0 && len(all) > limit {
			return all[len(all)-limit:], nil
		}
		return all, nil
	}
	out := make([]Message, 0, len(all))
	for _, m := range all {
		if m.Seq > afterSeq {
			out = append(out, m)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// History returns a thread's messages in the order they were said.
//
// It sorts on seq, not created_at: two messages written in the same
// millisecond by the same turn would otherwise be free to come back in either
// order, and a swapped user/agent pair silently corrupts every later turn
// of the conversation.
func (h *Hub) History(threadID string, limit int) ([]Message, error) {
	q := url.Values{}
	q.Set("thread_id", threadID)
	q.Set("order_by", "seq")
	q.Set("order", "asc")
	var recs []bkn.Record
	var err error
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
		recs, err = h.C.List(RefMessages, q)
	} else {
		recs, err = h.listAll(RefMessages, q)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(recs))
	for _, r := range recs {
		out = append(out, messageFrom(r))
	}
	return out, nil
}

// AppendMessage stores one turn.
func (h *Hub) AppendMessage(m Message) (Message, error) {
	doc := map[string]any{
		"thread_id":  m.ThreadID,
		"role":       m.Role,
		"content":    m.Content,
		"seq":        m.Seq,
		"created_at": now(),
	}
	if m.Model != "" {
		doc["model"] = m.Model
	}
	if m.Prompt > 0 {
		doc["prompt_tokens"] = m.Prompt
	}
	if m.Completed > 0 {
		doc["completion_tokens"] = m.Completed
	}
	if m.ToolCalls != "" {
		doc["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != "" {
		doc["tool_call_id"] = m.ToolCallID
	}
	if m.Name != "" {
		doc["name"] = m.Name
	}
	rec, err := h.C.Create(RefMessages, doc)
	if err != nil {
		return Message{}, err
	}
	return messageFrom(rec), nil
}

// DeleteThread removes a thread and its messages. The messages go first: a
// crash halfway leaves orphaned messages under a thread that still lists them,
// which is recoverable, where the reverse leaves messages nothing can reach.
func (h *Hub) DeleteThread(id string) (int, error) {
	msgs, err := h.History(id, 0)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, m := range msgs {
		if err := h.C.Delete(RefMessages, m.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, h.C.Delete(RefThreads, id)
}

// titleFrom derives a thread title from its opening message, so a thread list
// is readable without anyone having had to name anything.
func titleFrom(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 60
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndex(s[:max], " ")
	if cut < 20 {
		cut = max
	}
	return s[:cut] + "..."
}
