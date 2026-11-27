package main

import (
	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

func agentCmd(fl *flags) {
	// Built per case, so a bad subcommand is reported as one rather than as a
	// missing token.
	client := func() *hub.Hub { h, _ := hubFor(fl); return h }
	switch fl.arg(0) {
	case "create":
		agentCreate(client(), fl)
	case "", "list", "ls":
		agentList(client(), fl)
	case "get", "show":
		h := client()
		out.Data(map[string]any{"agent": resolveAgent(h, agentRef(fl))})
	case "update":
		agentUpdate(client(), fl)
	case "rm", "delete":
		agentDelete(client(), fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"agent takes create, list, get, update, rm")
	}
}

func agentCreate(h *hub.Hub, fl *flags) {
	temp, ok := fl.floatPtr("temperature")
	if !ok {
		out.Fail(out.InvalidValue, "invalid_value", "--temperature must be a number")
	}
	// --model is a global flag: it also selects an ad-hoc model for one chat
	// turn, so it is parsed once rather than declared per command.
	a, err := h.CreateAgent(hub.Agent{
		Name:          fl.str("name"),
		Model:         fl.model,
		System:        fl.str("system"),
		Provider:      fl.str("provider-name"),
		MemoryProject: fl.str("memory-project"),
		Skills:        splitList(fl.str("skills")),
		Tools:         splitList(fl.str("tools")),
		Temperature:   temp,
		MaxTokens:     fl.intVal("max-tokens", 0),
	})
	if err != nil {
		if !bkn.FromBackend(err) {
			out.Fail(out.ValidationError, "validation_error", err.Error(),
				"ahub agent create --name helper --model mistral-small-latest")
		}
		fail(err, "create agent")
	}
	out.Data(map[string]any{"agent": a})
}

func agentList(h *hub.Hub, fl *flags) {
	list, err := h.ListAgents(fl.intVal("limit", 100))
	if err != nil {
		fail(err, "list agents")
	}
	out.Data(map[string]any{"count": len(list), "agents": list})
}

func agentUpdate(h *hub.Hub, fl *flags) {
	a := resolveAgent(h, agentRef(fl))
	fields := map[string]any{}
	if v := fl.str("name"); v != "" {
		fields["name"] = v
	}
	if v := fl.str("system"); v != "" {
		fields["system"] = v
	}
	// These three are clearable: passing --memory-project "" removes it.
	// Testing the flag's presence rather than its value is what makes
	// "unset this" expressible at all -- without it a memory project, once
	// set, could never be taken away except by deleting the agent.
	if fl.has("memory-project") {
		fields["memory_project"] = fl.str("memory-project")
	}
	if fl.has("skills") {
		fields["skills"] = splitList(fl.str("skills"))
	}
	if fl.has("tools") {
		fields["tools"] = splitList(fl.str("tools"))
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
	updated, err := h.UpdateAgent(a.ID, fields)
	if err != nil {
		if !bkn.FromBackend(err) {
			out.Fail(out.ValidationError, "validation_error", err.Error())
		}
		fail(err, "update agent")
	}
	out.Data(map[string]any{"agent": updated})
}

func agentDelete(h *hub.Hub, fl *flags) {
	a := resolveAgent(h, agentRef(fl))
	if err := h.DeleteAgent(a.ID); err != nil {
		fail(err, "delete agent")
	}
	out.Data(map[string]any{"deleted": a.ID, "name": a.Name})
}

// agentRef reads the agent from either the positional argument or
// --agent, so both `agent get helper` and `--agent helper` work.
func agentRef(fl *flags) string {
	if v := fl.str("agent"); v != "" {
		return v
	}
	return fl.arg(1)
}

// resolveAgent turns a name or id into an agent, exiting with the
// right code when it cannot. A name that matches nothing is 92 (not found),
// never an external error an agent might sit and retry.
func resolveAgent(h *hub.Hub, ref string) hub.Agent {
	if ref == "" {
		out.Fail(out.ValidationError, "validation_error",
			"name an agent", "ahub agent list")
	}
	a, err := h.ResolveAgent(ref)
	if err != nil {
		if !bkn.FromBackend(err) {
			out.Fail(out.NotFound, "not_found", err.Error(), "ahub agent list")
		}
		fail(err, "resolve agent")
	}
	return a
}
