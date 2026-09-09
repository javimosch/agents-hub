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
		BaseURL: cfg.ProviderURL, APIKey: cfg.ProviderKey, Timeout: cfg.timeout(),
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
		if ctx.Err() != nil {
			out.Fail(out.ExternalError, "cancelled",
				"interrupted before the model answered; the question is stored",
				"ahub chat --thread <id> --message <same message>")
		}
		// Anything else at this point came from the model provider.
		out.Fail(out.ExternalError, "provider_error", err.Error(),
			"retry, or try --model with a different model")
	}

	out.Data(map[string]any{
		"thread":     turn.Thread.ID,
		"assistant":  a.Name,
		"model":      turn.Model,
		"content":    turn.Assistant.Content,
		"message_id": turn.Assistant.ID,
		"seq":        turn.Assistant.Seq,
		"usage":      turn.Usage,
		"finish":     turn.Finish,
	})
	if fl.human {
		out.Log("--- %s (%s) thread %s", a.Name, turn.Model, turn.Thread.ID)
	}
}
