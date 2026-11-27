package hub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/provider"
)

// Turn is everything one exchange produced.
type Turn struct {
	Thread   Thread         `json:"thread"`
	User     Message        `json:"user_message"`
	Reply    Message        `json:"reply"`
	Model    string         `json:"model"`
	Usage    provider.Usage `json:"usage"`
	Finish   string         `json:"finish_reason,omitempty"`
	Memory   *memory.Block  `json:"memory,omitempty"`
	Skills   *SkillReport   `json:"skills,omitempty"`
	Steps    int            `json:"steps,omitempty"`
	ToolRuns []ToolRun      `json:"tool_runs,omitempty"`
	// Compaction is present whenever a turn folded the thread down, so a
	// caller can see that history was summarised rather than discovering it
	// later from a conversation that has quietly lost its middle.
	Compaction *CompactReport `json:"compaction,omitempty"`
}

// ToolRun is one executed tool call, reported so an agent can see what the
// answer was actually built from.
type ToolRun struct {
	Step   int    `json:"step"`
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`
	Chars  int    `json:"chars,omitempty"`
	Failed string `json:"failed,omitempty"`
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
	Agent   Agent
	Thread  *Thread // nil opens a new one
	Message string
	// HistoryLimit caps how many prior messages are replayed to the model. It
	// is a blunt window, not compaction: a real compaction strategy belongs in
	// the agentic engine, and pretending a truncated head is a summary would
	// hide the loss instead of bounding it.
	HistoryLimit int
	OnDelta      func(string)
	// OnEvent, when set, is told when the question is stored and around each
	// tool run.
	OnEvent func(Event)
	// KeepAlive, when set, runs before each model call so a caller can renew
	// what it holds. An error ends the turn.
	KeepAlive func() error

	// Memory, when set, recalls from a memgraph project and prepends the
	// result as a second system message. It is the deliberate alternative to
	// a vector store: the memories are written on purpose, readable as text,
	// and owned by a tool that already exists.
	Memory *MemoryOptions

	// Skills names procedures to load and prepend. Resolution and reading
	// happen through the memory client, which is the only thing here allowed
	// to touch the filesystem.
	Skills *SkillOptions

	// Tools, when set, lets the model ask for work between the question and
	// the answer. MaxSteps bounds the loop: a model that keeps calling tools
	// is a model spending money, and "it will stop eventually" is not a
	// budget.
	Tools    *Registry
	MaxSteps int

	// NoStore runs a turn without creating a thread or recording anything.
	// It exists for work that is about a conversation rather than part of one
	// -- summarising it, judging it -- which should not appear in it.
	NoStore bool
	// Compact folds the thread down before the turn when its measured prompt
	// size has crossed the threshold, and again if the provider refuses the
	// prompt anyway.
	Compact *CompactPolicy
	// DailyBudget refuses a turn once the day's token allowance is spent, and
	// records what this turn cost. Zero means unlimited, which is the right
	// default for a person at a terminal and the wrong one for a schedule.
	DailyBudget int
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

	if opts.NoStore {
		return h.oneShot(ctx, p, opts)
	}

	if opts.DailyBudget > 0 {
		if err := h.CheckBudget(opts.DailyBudget); err != nil {
			return nil, err
		}
	}

	// Every provider call below adds to spent, and the deferred write records
	// it however the turn ends. The turns that hit a step cap or fail partway
	// are the expensive ones a budget exists to count.
	spent := 0
	if opts.DailyBudget > 0 {
		// Bookkeeping must not turn an answered question into a failure.
		defer func() { _ = h.RecordSpend(spent) }()
	}

	th := opts.Thread
	if th == nil {
		created, err := h.CreateThread(opts.Agent, titleFrom(opts.Message))
		if err != nil {
			return nil, err
		}
		th = &created
	}

	// One turn per thread: two would number their messages alike and weave
	// their tool calls together, and neither could be replayed.
	lease, err := h.acquire(RefThreads, th.ID, Owner(), LeaseTTL)
	if err != nil {
		return nil, err
	}
	if !lease.Held {
		return nil, &BusyError{Holder: lease.HeldBy}
	}
	// A failed release costs a wait for the lease to be judged abandoned.
	defer func() { _ = h.release(lease) }()
	// Read again now that nobody else can be changing it.
	current, err := h.GetThread(th.ID)
	if err != nil {
		return nil, err
	}
	*th = current

	// Compaction happens before the history is read, so the turn is built
	// from the folded thread rather than the one that no longer fits.
	var compaction *CompactReport
	if opts.Compact != nil && opts.Compact.Due(th, opts.Agent.Model) {
		rep, err := h.Compact(ctx, p, th, opts.Agent, *opts.Compact,
			fmt.Sprintf("prompt reached %d tokens, %d%% of this model's window",
				th.LastPrompt, int(100*float64(th.LastPrompt)/float64(opts.Compact.LimitFor(opts.Agent.Model)))))
		if err != nil {
			// A failed compaction is reported and the turn goes ahead: the
			// prompt may still fit, and refusing to answer because the
			// housekeeping failed would be the wrong trade.
			rep.Error = err.Error()
		}
		spent += rep.Tokens
		compaction = rep
	}

	history, err := h.HistoryAfter(th.ID, th.SummarySeq, opts.HistoryLimit)
	if err != nil {
		return nil, err
	}

	block, err := recallFor(opts)
	if err != nil {
		return nil, err
	}
	skills, skillText, err := loadSkills(opts)
	if err != nil {
		return nil, err
	}
	msgs := buildPrompt(opts.Agent, th, block, skillText, history)
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

	// Sent as soon as there is something to resume: a turn that dies later
	// has already named its thread.
	opts.emit(Event{Type: "start", Thread: th.ID})

	// From here on the context knows which thread it belongs to, so a tool
	// that records something can say what asked for it.
	ctx = WithThread(ctx, th.ID)
	turn := &Turn{Thread: *th, User: userMsg, Memory: block, Skills: skills, Compaction: compaction}
	next := seq + 1
	compacted := false
	maxSteps := opts.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 8
	}
	var specs []provider.Tool
	if opts.Tools != nil {
		specs = opts.Tools.Specs()
	}

	for step := 1; ; step++ {
		if err := h.keepAlive(&lease, opts); err != nil {
			return nil, err
		}
		resp, err := p.Chat(ctx, provider.Request{
			Model:       opts.Agent.Model,
			Messages:    msgs,
			Temperature: opts.Agent.Temperature,
			MaxTokens:   opts.Agent.MaxTokens,
			Tools:       specs,
		}, opts.OnDelta)
		if ce, isCtx := provider.IsContextError(err); isCtx && opts.Compact != nil && !compacted {
			// The backstop. The threshold is an estimate of somebody else's
			// tokeniser; this is the provider telling us the real answer, so
			// the window it names is remembered and the thread is folded and
			// tried once more. Once, and reported: a silent retry loop around
			// a full context window is a way to spend money on nothing.
			compacted = true
			if ce.Limit > 0 {
				if opts.Compact.Limits != nil {
					opts.Compact.Limits[opts.Agent.Model] = ce.Limit
				}
				if opts.Compact.OnLearn != nil {
					opts.Compact.OnLearn(opts.Agent.Model, ce.Limit)
				}
			}
			th.LastPrompt = ce.Used
			rep, cerr := h.Compact(ctx, p, th, opts.Agent, *opts.Compact, "the provider refused the prompt: "+ce.Error())
			spent += rep.Tokens
			if cerr == nil && rep.Compacted {
				compaction = rep
				rebuilt, herr := h.rebuild(opts, th, block, skillText)
				if herr != nil {
					return nil, herr
				}
				msgs = rebuilt
				continue
			}
			// Compaction could not rescue it. That usually means the fixed
			// parts -- the system prompt, a recalled block, a skill -- are
			// already close to the window, and no amount of folding the
			// conversation will help. Saying that is far more use than
			// "provider error, try again".
			return nil, &UnfittableError{Ctx: ce, Why: compactWhy(rep, cerr)}
		}
		if ce, isCtx := provider.IsContextError(err); isCtx && compacted {
			// Folded once already and it still does not fit. Retrying is not
			// the answer and neither is folding again, so say what would
			// actually help rather than inviting another attempt.
			return nil, &UnfittableError{Ctx: ce,
				Why: "the thread was compacted and the prompt still does not fit"}
		}
		if err != nil {
			return nil, err
		}
		if resp.Usage.PromptTokens > 0 {
			th.LastPrompt = resp.Usage.PromptTokens
		}
		spent += resp.Usage.TotalTokens
		turn.Steps = step
		model := resp.Model
		if model == "" {
			model = opts.Agent.Model
		}
		turn.Model = model
		// Usage accumulates across the loop. Reporting only the last call
		// would understate a five-step turn by most of its cost.
		turn.Usage.PromptTokens += resp.Usage.PromptTokens
		turn.Usage.CompletionTokens += resp.Usage.CompletionTokens
		turn.Usage.TotalTokens += resp.Usage.TotalTokens
		turn.Finish = resp.FinishReason

		// The agent turn is stored whether it answered or asked for work,
		// so a resumed thread replays the tool calls that a later tool result
		// refers to. A provider rejects an orphaned tool result outright.
		calls := ""
		if len(resp.ToolCalls) > 0 {
			b, err := json.Marshal(resp.ToolCalls)
			if err != nil {
				return nil, err
			}
			calls = string(b)
		}
		asstMsg, err := h.AppendMessage(Message{
			ThreadID: th.ID, Role: provider.RoleAssistant, Content: resp.Content,
			Model: model, Seq: next, ToolCalls: calls,
			Prompt: resp.Usage.PromptTokens, Completed: resp.Usage.CompletionTokens,
		})
		if err != nil {
			return nil, err
		}
		next++
		if err := h.touch(th, next); err != nil {
			return nil, err
		}
		msgs = append(msgs, provider.Message{
			Role: provider.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			turn.Reply = asstMsg
			return turn, nil
		}
		if opts.Tools == nil {
			// The model asked for a tool that was never offered. Stopping is
			// the honest move: presenting this as an answer would hide that
			// it is a request nobody answered.
			if err := h.answerUnrun(th, next, resp.ToolCalls, "no tools are enabled"); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("model requested tools but none are enabled")
		}
		if step >= maxSteps {
			// The cap is a budget, so hitting it is reported rather than
			// smoothed over: finish_reason says max_steps and the last
			// agent turn is whatever the model had produced.
			if err := h.answerUnrun(th, next, resp.ToolCalls, "the step limit was reached"); err != nil {
				return nil, err
			}
			turn.Thread = *th
			turn.Reply = asstMsg
			turn.Finish = "max_steps"
			return turn, nil
		}

		for _, call := range resp.ToolCalls {
			opts.emit(Event{Type: "tool_call", Step: step, ID: call.ID,
				Name: call.Name, Args: call.Arguments})
			result, run := h.runTool(ctx, opts.Tools, step, call)
			opts.emit(Event{Type: "tool_result", Step: step, ID: call.ID,
				Name: call.Name, Chars: run.Chars, Failed: run.Failed})
			turn.ToolRuns = append(turn.ToolRuns, run)
			if _, err := h.AppendMessage(Message{
				ThreadID: th.ID, Role: provider.RoleTool, Content: result,
				Seq: next, ToolCallID: call.ID, Name: call.Name,
			}); err != nil {
				return nil, err
			}
			next++
			msgs = append(msgs, provider.Message{
				Role: provider.RoleTool, Content: result,
				ToolCallID: call.ID, Name: call.Name,
			})
		}
		if err := h.touch(th, next); err != nil {
			return nil, err
		}
	}
}

// keepAlive renews the leases a turn depends on before it spends more.
func (h *Hub) keepAlive(thread *Lease, opts ChatOptions) error {
	if err := h.RenewLease(thread); err != nil {
		return err
	}
	if opts.KeepAlive != nil {
		return opts.KeepAlive()
	}
	return nil
}

// UnfittableError is a prompt that does not fit and cannot be made to fit by
// folding the conversation.
type UnfittableError struct {
	Ctx *provider.ContextError
	Why string
}

func (e *UnfittableError) Error() string {
	return e.Ctx.Error() + "; compaction could not help: " + e.Why
}

func compactWhy(rep *CompactReport, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case rep != nil && rep.Error != "":
		return rep.Error
	default:
		return "there was nothing left to fold"
	}
}

// oneShot answers without a thread, for work about a conversation rather than
// in it. Nothing is stored, so nothing later has to explain why a summary is
// sitting in the middle of a transcript.
func (h *Hub) oneShot(ctx context.Context, p provider.Provider, opts ChatOptions) (*Turn, error) {
	msgs := []provider.Message{}
	if opts.Agent.System != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: opts.Agent.System})
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: opts.Message})
	resp, err := p.Chat(ctx, provider.Request{
		Model: opts.Agent.Model, Messages: msgs,
		Temperature: opts.Agent.Temperature, MaxTokens: opts.Agent.MaxTokens,
	}, nil)
	if err != nil {
		return nil, err
	}
	model := resp.Model
	if model == "" {
		model = opts.Agent.Model
	}
	return &Turn{
		Reply: Message{Role: provider.RoleAssistant, Content: resp.Content, Model: model},
		Model: model, Usage: resp.Usage, Finish: resp.FinishReason, Steps: 1,
	}, nil
}

// touch records how many messages the thread now holds and when it last moved.
func (h *Hub) touch(th *Thread, count int) error {
	th.Messages = count
	fields := map[string]any{"messages": count, "updated_at": now()}
	// The measured prompt size rides along with the message count, so the
	// next turn knows how close to the window this thread already is without
	// having to estimate anything.
	if th.LastPrompt > 0 {
		fields["last_prompt_tokens"] = th.LastPrompt
	}
	_, err := h.C.Patch(RefThreads, th.ID, fields)
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
