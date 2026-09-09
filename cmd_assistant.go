package main

import (
	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

func assistantCmd(fl *flags) {
	h, _ := hubFor(fl)
	switch fl.arg(0) {
	case "create":
		assistantCreate(h, fl)
	case "", "list", "ls":
		assistantList(h, fl)
	case "get", "show":
		out.Data(map[string]any{"assistant": resolveAssistant(h, assistantRef(fl))})
	case "update":
		assistantUpdate(h, fl)
	case "rm", "delete":
		assistantDelete(h, fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"assistant takes create, list, get, update, rm")
	}
}

func assistantCreate(h *hub.Hub, fl *flags) {
	temp, ok := fl.floatPtr("temperature")
	if !ok {
		out.Fail(out.InvalidValue, "invalid_value", "--temperature must be a number")
	}
	// --model is a global flag: it also selects an ad-hoc model for one chat
	// turn, so it is parsed once rather than declared per command.
	a, err := h.CreateAssistant(hub.Assistant{
		Name:          fl.str("name"),
		Model:         fl.model,
		System:        fl.str("system"),
		Provider:      fl.str("provider-name"),
		MemoryProject: fl.str("memory-project"),
		Skills:        splitList(fl.str("skills")),
		Temperature:   temp,
		MaxTokens:     fl.intVal("max-tokens", 0),
	})
	if err != nil {
		if !bkn.FromBackend(err) {
			out.Fail(out.ValidationError, "validation_error", err.Error(),
				"ahub assistant create --name helper --model mistral-small-latest")
		}
		fail(err, "create assistant")
	}
	out.Data(map[string]any{"assistant": a})
}

func assistantList(h *hub.Hub, fl *flags) {
	list, err := h.ListAssistants(fl.intVal("limit", 100))
	if err != nil {
		fail(err, "list assistants")
	}
	out.Data(map[string]any{"count": len(list), "assistants": list})
}

func assistantUpdate(h *hub.Hub, fl *flags) {
	a := resolveAssistant(h, assistantRef(fl))
	fields := map[string]any{}
	if v := fl.str("name"); v != "" {
		fields["name"] = v
	}
	if v := fl.str("system"); v != "" {
		fields["system"] = v
	}
	if v := fl.str("memory-project"); v != "" {
		fields["memory_project"] = v
	}
	if v := fl.str("skills"); v != "" {
		fields["skills"] = splitList(v)
	}
	if fl.model != "" {
		fields["model"] = fl.model
	}
	if t, ok := fl.floatPtr("temperature"); !ok {
		out.Fail(out.InvalidValue, "invalid_value", "--temperature must be a number")
	} else if t != nil {
		fields["temperature"] = *t
	}
	if n := fl.intVal("max-tokens", 0); n > 0 {
		fields["max_tokens"] = n
	}
	updated, err := h.UpdateAssistant(a.ID, fields)
	if err != nil {
		if !bkn.FromBackend(err) {
			out.Fail(out.ValidationError, "validation_error", err.Error())
		}
		fail(err, "update assistant")
	}
	out.Data(map[string]any{"assistant": updated})
}

func assistantDelete(h *hub.Hub, fl *flags) {
	a := resolveAssistant(h, assistantRef(fl))
	if err := h.DeleteAssistant(a.ID); err != nil {
		fail(err, "delete assistant")
	}
	out.Data(map[string]any{"deleted": a.ID, "name": a.Name})
}

// assistantRef reads the assistant from either the positional argument or
// --assistant, so both `assistant get helper` and `--assistant helper` work.
func assistantRef(fl *flags) string {
	if v := fl.str("assistant"); v != "" {
		return v
	}
	return fl.arg(1)
}

// resolveAssistant turns a name or id into an assistant, exiting with the
// right code when it cannot. A name that matches nothing is 92 (not found),
// never an external error an agent might sit and retry.
func resolveAssistant(h *hub.Hub, ref string) hub.Assistant {
	if ref == "" {
		out.Fail(out.ValidationError, "validation_error",
			"name an assistant", "ahub assistant list")
	}
	a, err := h.ResolveAssistant(ref)
	if err != nil {
		if !bkn.FromBackend(err) {
			out.Fail(out.NotFound, "not_found", err.Error(), "ahub assistant list")
		}
		fail(err, "resolve assistant")
	}
	return a
}
