package hub

import (
	"context"
	"fmt"

	"github.com/javimosch/agents-hub/internal/memory"
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
	Memory    *memory.Block  `json:"memory,omitempty"`
	Skills    *SkillReport   `json:"skills,omitempty"`
}

// SkillReport says which skills actually made it into the prompt. An answer
// given without the procedure it was supposed to follow must not look like
// one given with it.
type SkillReport struct {
	Loaded  []string          `json:"loaded,omitempty"`
	Chars   int               `json:"chars,omitempty"`
	Skipped map[string]string `json:"skipped,omitempty"`
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

	// Memory, when set, recalls from a memgraph project and prepends the
	// result as a second system message. It is the deliberate alternative to
	// a vector store: the memories are written on purpose, readable as text,
	// and owned by a tool that already exists.
	Memory *MemoryOptions

	// Skills names procedures to load and prepend. Resolution and reading
	// happen through the memory client, which is the only thing here allowed
	// to touch the filesystem.
	Skills *SkillOptions
}

// SkillOptions is the set of skills to load for this turn.
type SkillOptions struct {
	Client *memory.Client
	Names  []string
	// Required fails the turn when a named skill cannot be loaded. Default is
	// off for the same reason as memory: a missing procedure should degrade
	// the answer, not withhold it. Naming one explicitly is a different
	// matter and the CLI treats it as such.
	Required bool
}

// MemoryOptions is the recall to run before the turn.
type MemoryOptions struct {
	Client  *memory.Client
	Project string
	Query   string // empty means the user's message
	Limit   int
	Budget  memory.Budget
	// Required makes a failed recall fail the turn. Off by default: a memory
	// store being down should degrade an answer, not withhold it.
	Required bool
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

	msgs := make([]provider.Message, 0, len(history)+3)
	if opts.Assistant.System != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: opts.Assistant.System})
	}
	block, err := recallFor(opts)
	if err != nil {
		return nil, err
	}
	// The memory block sits after the assistant's own instructions and before
	// the conversation: the prompt still outranks it, and it is not mistaken
	// for something the user said.
	if block != nil && block.Text != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: block.Text})
	}
	skills, skillText, err := loadSkills(opts)
	if err != nil {
		return nil, err
	}
	if skillText != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: skillText})
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
		Memory: block, Skills: skills,
	}, nil
}

// loadSkills resolves each named skill through memgraph and reads its file.
func loadSkills(opts ChatOptions) (*SkillReport, string, error) {
	so := opts.Skills
	names := append([]string{}, opts.Assistant.Skills...)
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

// dedupe keeps the first occurrence of each name. An assistant configured
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
