package hub

import (
	"encoding/json"
	"fmt"

	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/provider"
)

// rebuild reassembles the prompt after a compaction mid-turn. The turn's own
// messages are already stored, so history supplies them.
func (h *Hub) rebuild(opts ChatOptions, th *Thread, block *memory.Block, skillText string) ([]provider.Message, error) {
	history, err := h.HistoryAfter(th.ID, th.SummarySeq, opts.HistoryLimit)
	if err != nil {
		return nil, err
	}
	return buildPrompt(opts.Agent, th, block, skillText, history), nil
}

// buildPrompt lays out system context, then the replayed conversation. The
// memory block sits after the agent's own instructions and before the
// conversation: the instructions still outrank it, and it is not mistaken for
// something the user said. The summary is system context for the same reason.
func buildPrompt(a Agent, th *Thread, block *memory.Block, skillText string, history []Message) []provider.Message {
	system := []string{a.System}
	if th.Summary != "" {
		system = append(system, "## Earlier in this conversation\n\n"+th.Summary)
	}
	if block != nil {
		system = append(system, block.Text)
	}
	system = append(system, skillText)

	msgs := make([]provider.Message, 0, len(history)+len(system)+1)
	for _, s := range system {
		if s != "" {
			msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: s})
		}
	}
	// A --history window can open partway through a tool exchange. A result
	// whose call was cut off is rejected by providers, so it is cut too.
	for len(history) > 0 && history[0].Role == provider.RoleTool {
		history = history[1:]
	}
	for _, m := range history {
		msgs = append(msgs, toProvider(m))
	}
	return msgs
}

// toProvider rebuilds a stored message for replay, including the tool call
// linkage. A dropped tool_calls array makes the next tool result an orphan
// and the whole conversation unreplayable.
func toProvider(m Message) provider.Message {
	out := provider.Message{
		Role: m.Role, Content: m.Content,
		ToolCallID: m.ToolCallID, Name: m.Name,
	}
	if m.ToolCalls != "" {
		var calls []provider.ToolCall
		if err := json.Unmarshal([]byte(m.ToolCalls), &calls); err == nil {
			out.ToolCalls = calls
		}
	}
	return out
}

// loadSkills resolves each named skill through memgraph and reads its file.
func loadSkills(opts ChatOptions) (*SkillReport, string, error) {
	so := opts.Skills
	names := append([]string{}, opts.Agent.Skills...)
	if so != nil {
		names = append(names, so.Names...)
	}
	names = dedupe(names)
	if len(names) == 0 || so == nil || so.Client == nil {
		return nil, "", nil
	}
	rep := &SkillReport{}
	loaded := map[string]string{}
	order := make([]string, 0, len(names))
	for _, name := range names {
		sk, err := so.Client.FindSkill(name, 10)
		if err == nil {
			var body string
			body, err = memory.LoadSkill(sk)
			if err == nil {
				loaded[name] = body
				order = append(order, name)
				rep.Loaded = append(rep.Loaded, name)
				continue
			}
		}
		if so.Required {
			return nil, "", fmt.Errorf("skill %q: %w", name, err)
		}
		if rep.Skipped == nil {
			rep.Skipped = map[string]string{}
		}
		rep.Skipped[name] = err.Error()
	}
	text := memory.SkillBlock(loaded, order)
	rep.Chars = len(text)
	return rep, text, nil
}

// dedupe keeps the first occurrence of each name. An agent configured
// with a skill and then asked for it again on the command line should not
// pay for it twice.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// recallFor runs the pre-turn recall, if one was asked for.
//
// A recall failure is reported on the turn rather than raised, unless the
// caller said the memory was required. Losing context degrades an answer;
// refusing to answer at all because a side lookup failed is worse than the
// answer would have been.
func recallFor(opts ChatOptions) (*memory.Block, error) {
	m := opts.Memory
	if m == nil || m.Client == nil {
		return nil, nil
	}
	q := m.Query
	if q == "" {
		q = opts.Message
	}
	res, err := m.Client.Recall(memory.Query{
		Text: q, Project: m.Project, Limit: m.Limit, Format: "index",
	})
	if err != nil {
		if m.Required {
			return nil, fmt.Errorf("memory recall failed: %w", err)
		}
		return &memory.Block{Project: m.Project, Error: err.Error()}, nil
	}
	b := memory.Context(res, m.Project, m.Budget)
	return &b, nil
}
