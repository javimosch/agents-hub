package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/out"
	"github.com/javimosch/agents-hub/internal/provider"
)

func chatCmd(fl *flags) {
	h, cfg := hubFor(fl)

	msg := fl.str("message")
	if msg == "" {
		msg = strings.Join(fl.rest, " ")
	}
	if strings.TrimSpace(msg) == "" {
		out.Fail(out.ValidationError, "validation_error",
			"--message is required",
			`ahub chat --agent helper --message "what changed today?"`)
	}

	a := resolveAgent(h, fl.str("agent"))
	// A per-call --model overrides the agent's, so an agent can try a
	// bigger model on a hard turn without editing (and then having to undo)
	// the saved configuration.
	if fl.model != "" {
		a.Model = fl.model
	}

	var th *hub.Thread
	if id := fl.str("thread"); id != "" {
		t, err := h.GetThread(id)
		if err != nil {
			if bkn.NotFound(err) {
				out.Fail(out.NotFound, "not_found", "no thread "+id, "ahub thread list")
			}
			fail(err, "load thread")
		}
		th = &t
	}

	name := a.Provider
	if name == "" {
		name = cfg.Provider
	}
	p, err := provider.New(name, provider.Config{
		BaseURL: cfg.ProviderURL, APIKey: cfg.ProviderKey, Timeout: cfg.providerTimeout(),
	})
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error(),
			"ahub config set --provider openai --provider-url https://api.mistral.ai/v1")
	}

	// Ctrl-C cancels the provider call rather than the process, so the user
	// message already stored stays consistent with a thread that has no reply
	// yet, instead of dying mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := hub.ChatOptions{
		Agent:        a,
		Thread:       th,
		Message:      msg,
		HistoryLimit: fl.intVal("history", 100),
		Memory:       memoryFor(fl, a),
		Skills:       skillsFor(fl, a),
		Tools:        toolsFor(fl, cfg, h, a),
		Compact:      compactionFor(fl, cfg, h),
		DailyBudget:  cfg.DailyTokenBudget,
		MaxSteps:     fl.intVal("max-steps", 0),
	}
	if fl.stream {
		// Deltas are NDJSON on stdout, not bare text: stdout stays parseable,
		// and a reader can consume the answer as it arrives without having to
		// guess where the data ends and the envelope begins.
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		opts.OnDelta = func(s string) {
			_ = enc.Encode(map[string]any{"type": "delta", "text": s})
		}
		opts.OnEvent = func(e hub.Event) { _ = enc.Encode(e) }
	}

	turn, err := h.Chat(ctx, p, opts)
	if err != nil {
		if bkn.FromBackend(err) {
			fail(err, "chat")
		}
		var overspent *hub.BudgetError
		if errors.As(err, &overspent) {
			// Not a provider problem and not worth retrying today. 95 says
			// "the state conflicts with what you asked", which is exactly
			// what a spent budget is.
			out.Fail(out.Conflict, "budget_spent", err.Error(),
				"raise it with `ahub config set --daily-token-budget <n>`, or wait for tomorrow",
				"ahub spend")
		}
		if strings.HasPrefix(err.Error(), "model requested tools") {
			out.Fail(out.ValidationError, "tools_disabled", err.Error(),
				"enable tools with --tools memory_recall,skill_show")
		}
		if strings.HasPrefix(err.Error(), "skill ") {
			out.Fail(out.NotFound, "skill_error", err.Error(),
				`ahub skill search "..."`, "or drop --skills-required")
		}
		if ctx.Err() != nil {
			out.Fail(out.ExternalError, "cancelled",
				"interrupted before the model answered; the question is stored",
				"ahub chat --thread <id> --message <same message>")
		}
		var unfit *hub.UnfittableError
		if errors.As(err, &unfit) {
			// Not recoverable by retrying: the same prompt will not fit the
			// next time either. What would help is a shorter system prompt, a
			// smaller memory budget, or a model with a bigger window.
			out.Fail(out.InvalidValue, "prompt_too_large", err.Error(),
				"shorten the agent's system prompt, lower --memory-chars, or use a model with a larger context window",
				"ahub config set --model-limit <model>=<tokens> if the window is known")
		}
		// Anything else at this point came from the model provider.
		out.Fail(out.ExternalError, "provider_error", err.Error(),
			"retry, or try --model with a different model")
	}

	// A turn that ran out of steps without producing text is not a successful
	// answer, and reporting it as one is the worst outcome available: an
	// agent gets ok:true and an empty string, and carries on. The thread is
	// stored and resumable, so the error says how to continue it.
	if turn.Finish == "max_steps" && strings.TrimSpace(turn.Reply.Content) == "" {
		out.Fail(out.ExternalError, "max_steps_exhausted",
			fmt.Sprintf("the model was still calling tools after %d steps and never answered",
				turn.Steps),
			"ahub chat --thread "+turn.Thread.ID+" --max-steps 20 --message <same message>",
			"or narrow the question, or drop a tool it keeps misusing")
	}

	fields := map[string]any{
		"thread":     turn.Thread.ID,
		"agent":      a.Name,
		"model":      turn.Model,
		"content":    turn.Reply.Content,
		"message_id": turn.Reply.ID,
		"seq":        turn.Reply.Seq,
		"usage":      turn.Usage,
		"finish":     turn.Finish,
	}
	// Memory is reported whenever it was attempted, including when it found
	// nothing or failed: an answer given without the context it was supposed
	// to have must not look like an answer given with it.
	if turn.Memory != nil {
		fields["memory"] = turn.Memory
	}
	if turn.Skills != nil {
		fields["skills"] = turn.Skills
	}
	if turn.Compaction != nil {
		fields["compaction"] = turn.Compaction
	}
	// Always reported, even when it is 1. A turn that stopped at its step
	// cap has one step and a finish reason of max_steps, and omitting the
	// count there would hide the budget exactly where it was hit.
	fields["steps"] = turn.Steps
	if len(turn.ToolRuns) > 0 {
		fields["tool_runs"] = turn.ToolRuns
	}
	if fl.stream {
		fields["type"] = "result"
	}
	out.Data(fields)
	if fl.human {
		out.Log("--- %s (%s) thread %s", a.Name, turn.Model, turn.Thread.ID)
	}
}

// toolsFor builds the registry for this turn.
//
// Tools are opt-in twice over: named here or on the agent, and -- for
// anything that writes -- allowed again with --allow-writes. The second gate
// exists because "give it tools" and "let it change things" are different
// decisions that a single flag would collapse into one.
func toolsFor(fl *flags, cfg Config, h *hub.Hub, a hub.Agent) *hub.Registry {
	if fl.has("no-tools") {
		return nil
	}
	names := splitList(fl.str("tools"))
	if len(names) == 0 {
		names = a.Tools
	}
	if len(names) == 0 {
		return nil
	}
	// memgraph is needed only by the tools that use it: an agent given only
	// the cmd tool must run on a machine that has never heard of memgraph.
	var c *memory.Client
	if mc, err := memory.New(0); err == nil {
		c = mc
	} else if needsMemory(names) {
		out.Fail(out.NotFound, "memgraph_missing", err.Error(),
			"these tools are memgraph-backed; install it, drop them, or pass --no-tools")
	}
	project := fl.str("memory-project")
	if project == "" {
		project = a.MemoryProject
	}
	reg, err := hub.NewRegistry(hub.RegistryOptions{
		Names: names, Memory: c, Project: project,
		AllowWrites: fl.has("allow-writes"),
		Policy:      cfg.toolPolicy(), Timeout: cfg.timeout(),
		Hub: h,
	})
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error(), "ahub tool list")
	}
	return reg
}

// compactionFor builds the folding policy from the profile. It is on by
// default: a thread that silently stops fitting is a worse surprise than one
// that says it summarised itself, and --no-compact turns it off.
func compactionFor(fl *flags, cfg Config, h *hub.Hub) *hub.CompactPolicy {
	if fl.has("no-compact") {
		return nil
	}
	limits := map[string]int{}
	// What has been learned comes first, and an explicit --model-limit in the
	// profile overrides it: a number somebody typed on purpose beats one this
	// tool inferred from an error message.
	if h != nil {
		if known, err := h.LoadModelLimits(); err == nil {
			for k, v := range known {
				limits[k] = v
			}
		} else {
			out.Log("could not read known context windows: %v", err)
		}
	}
	for k, v := range cfg.ModelLimits {
		limits[k] = v
	}
	at := cfg.CompactAt
	if v := fl.str("compact-at"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f < 1 {
			at = f
		}
	}
	return &hub.CompactPolicy{
		Limits: limits, At: at, Keep: fl.intVal("compact-keep", 4),
		OnLearn: func(model string, limit int) { rememberLimit(h, model, limit) },
	}
}

// rememberLimit records a window a provider named.
//
// It goes to bkn rather than to the local profile, because it is a fact about
// the model rather than a policy of this machine -- so the next machine to
// use that model already knows, instead of rediscovering it by paying for
// another refused call. A failure to save is reported and ignored: the turn in
// flight already has the number it needs.
func rememberLimit(h *hub.Hub, model string, limit int) {
	if h == nil {
		return
	}
	if err := h.SaveModelLimit(model, limit, "learned"); err != nil {
		out.Log("learned %s = %d tokens but could not record it: %v", model, limit, err)
		return
	}
	out.Log("learned that %s has a %d-token context window", model, limit)
}

// needsMemory reports whether any named tool is memgraph-backed.
func needsMemory(names []string) bool {
	for _, n := range names {
		if n != "cmd" && !strings.HasPrefix(n, "job_") {
			return true
		}
	}
	return false
}

// memoryFor decides whether this turn recalls anything.
//
// --memory-project overrides the agent's; --no-memory turns it off for
// one turn without editing the agent. A missing memgraph binary is fatal
// only when memory was asked for explicitly on the command line -- an
// agent configured with a project on a machine without memgraph should
// still answer, and say that it did so without memory.
func memoryFor(fl *flags, a hub.Agent) *hub.MemoryOptions {
	if fl.has("no-memory") {
		return nil
	}
	project := fl.str("memory-project")
	explicit := project != ""
	if project == "" {
		project = a.MemoryProject
	}
	if project == "" {
		return nil
	}
	c, err := memory.New(0)
	if err != nil {
		if explicit {
			out.Fail(out.NotFound, "memgraph_missing", err.Error(),
				"install memgraph, point AHUB_MEMGRAPH at it, or pass --no-memory")
		}
		out.Log("memory: %v; answering without it", err)
		return nil
	}
	return &hub.MemoryOptions{
		Client:  c,
		Project: project,
		Limit:   fl.intVal("memory-limit", 5),
		Budget: memory.Budget{
			PerMemory: fl.intVal("memory-per", 0),
			Total:     fl.intVal("memory-chars", 0),
		},
		Required: fl.has("memory-required"),
	}
}
