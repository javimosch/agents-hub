package main

import (
	"strconv"
	"strings"

	"github.com/javimosch/agents-hub/internal/out"
)

// modelCmd shows and corrects what is known about the models themselves.
//
// This is shared knowledge rather than machine policy, so it lives in bkn and
// every machine on the account sees it. A window learned from one provider
// refusal is a window no other machine has to pay to discover.
func modelCmd(fl *flags) {
	switch fl.arg(0) {
	case "", "list", "ls":
		h, cfg := hubFor(fl)
		facts, err := h.ListModelFacts(fl.intVal("limit", 100))
		if err != nil {
			fail(err, "list model facts")
		}
		out.Data(map[string]any{
			"count": len(facts), "models": facts,
			// The profile's own entries are overrides, and saying so here
			// saves someone wondering why a number differs from bkn's.
			"local_overrides": cfg.ModelLimits,
		})
	case "set":
		h, _ := hubFor(fl)
		spec := fl.arg(1)
		if spec == "" {
			spec = fl.str("model")
		}
		name, val, ok := strings.Cut(spec, "=")
		if !ok {
			out.Fail(out.ValidationError, "validation_error",
				"give model=tokens", "ahub model set deepseek/deepseek-v4=128000")
		}
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n <= 0 {
			out.Fail(out.InvalidValue, "invalid_value", "the size must be a positive number of tokens")
		}
		if err := h.SaveModelLimit(strings.TrimSpace(name), n, "declared"); err != nil {
			fail(err, "record model limit")
		}
		out.Data(map[string]any{"model": strings.TrimSpace(name), "context_limit": n, "source": "declared"})
	default:
		out.Fail(out.InvalidArguments, "unknown_command", "model takes list or set")
	}
}
