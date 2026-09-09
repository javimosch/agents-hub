package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
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
		Tools:        toolsFor(fl, cfg, a),
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
	}

	turn, err := h.Chat(ctx, p, opts)
	if err != nil {
		if bkn.FromBackend(err) {
			fail(err, "chat")
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
	// Always reported, even when it is 1. A turn that stopped at its step
	// cap has one step and a finish reason of max_steps, and omitting the
	// count there would hide the budget exactly where it was hit.
	fields["steps"] = turn.Steps
	if len(turn.ToolRuns) > 0 {
		fields["tool_runs"] = turn.ToolRuns
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
func toolsFor(fl *flags, cfg Config, a hub.Agent) *hub.Registry {
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
		ToolCmds:    cfg.AllowToolCmds, Timeout: cfg.timeout(),
	})
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error(), "ahub tool list")
	}
	return reg
}

// needsMemory reports whether any named tool is memgraph-backed.
func needsMemory(names []string) bool {
	for _, n := range names {
		if n != "cmd" {
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
