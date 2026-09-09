package main

import (
	"context"
	"encoding/json"
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
			`ahub chat --assistant helper --message "what changed today?"`)
	}

	a := resolveAssistant(h, fl.str("assistant"))
	// A per-call --model overrides the assistant's, so an agent can try a
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
		Assistant:    a,
		Thread:       th,
		Message:      msg,
		HistoryLimit: fl.intVal("history", 100),
		Memory:       memoryFor(fl, a),
		Skills:       skillsFor(fl, a),
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

	fields := map[string]any{
		"thread":     turn.Thread.ID,
		"assistant":  a.Name,
		"model":      turn.Model,
		"content":    turn.Assistant.Content,
		"message_id": turn.Assistant.ID,
		"seq":        turn.Assistant.Seq,
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
	out.Data(fields)
	if fl.human {
		out.Log("--- %s (%s) thread %s", a.Name, turn.Model, turn.Thread.ID)
	}
}

// memoryFor decides whether this turn recalls anything.
//
// --memory-project overrides the assistant's; --no-memory turns it off for
// one turn without editing the assistant. A missing memgraph binary is fatal
// only when memory was asked for explicitly on the command line -- an
// assistant configured with a project on a machine without memgraph should
// still answer, and say that it did so without memory.
func memoryFor(fl *flags, a hub.Assistant) *hub.MemoryOptions {
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
