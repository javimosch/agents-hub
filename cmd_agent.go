package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/out"
	"github.com/javimosch/agents-hub/internal/provider"
)

func agentCmd(fl *flags) {
	switch fl.arg(0) {
	case "create":
		agentCreate(fl)
	case "", "list", "ls":
		agentList(fl)
	case "show", "get":
		agentShow(fl)
	case "rm", "delete":
		agentDelete(fl)
	case "enable":
		agentEnable(fl, true)
	case "disable":
		agentEnable(fl, false)
	case "run":
		agentRunOne(fl)
	case "due":
		agentDue(fl)
	case "tick":
		agentTick(fl)
	case "runs":
		agentRuns(fl)
	case "scratchpad":
		agentScratchpad(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"agent takes create, list, show, rm, enable, disable, run, due, tick, runs, scratchpad")
	}
}

func agentCreate(fl *flags) {
	h, _ := hubFor(fl)
	every := 0
	if v := fl.str("every"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			out.Fail(out.InvalidValue, "invalid_value",
				"--every takes a duration of at least 1m, like 15m or 6h")
		}
		every = int(d.Seconds())
	}
	if err := hub.ValidateAt(fl.str("at")); err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error())
	}
	a, err := h.CreateAgent(hub.Agent{
		Name: fl.str("name"), Assistant: fl.str("assistant"), Prompt: fl.str("prompt"),
		EverySecs: every, At: fl.str("at"),
		Tools:         splitList(fl.str("tools")),
		Skills:        splitList(fl.str("skills")),
		MemoryProject: fl.str("memory-project"),
		MaxSteps:      fl.intVal("max-steps", 0),
		TimeoutSecs:   fl.intVal("timeout", 0),
	})
	if err != nil {
		if !isBackend(err) {
			out.Fail(out.ValidationError, "validation_error", err.Error(),
				`ahub agent create --name nightly --assistant helper --prompt "..." --every 24h`)
		}
		fail(err, "create agent")
	}
	out.Data(map[string]any{"agent": a})
}

func agentList(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListAgents(fl.intVal("limit", 50))
	if err != nil {
		fail(err, "list agents")
	}
	out.Data(map[string]any{"count": len(list), "agents": list})
}

func agentShow(fl *flags) {
	h, _ := hubFor(fl)
	out.Data(map[string]any{"agent": resolveAgent(h, agentRef(fl))})
}

func agentDelete(fl *flags) {
	h, _ := hubFor(fl)
	a := resolveAgent(h, agentRef(fl))
	if err := h.DeleteAgent(a.ID); err != nil {
		fail(err, "delete agent")
	}
	out.Data(map[string]any{"deleted": a.ID, "name": a.Name})
}

func agentEnable(fl *flags, on bool) {
	h, _ := hubFor(fl)
	a := resolveAgent(h, agentRef(fl))
	fields := map[string]any{"enabled": on}
	// Re-arming a disabled agent computes a fresh due time, so it does not
	// fire immediately for every slot it slept through.
	if on {
		if next, err := a.NextRun(time.Now().UTC()); err == nil && !next.IsZero() {
			fields["next_run_at"] = next.Format(time.RFC3339)
		}
	}
	updated, err := h.UpdateAgent(a.ID, fields)
	if err != nil {
		fail(err, "update agent")
	}
	out.Data(map[string]any{"agent": updated})
}

func agentDue(fl *flags) {
	h, _ := hubFor(fl)
	due, err := h.DueAgents(time.Now().UTC())
	if err != nil {
		fail(err, "list due agents")
	}
	names := make([]string, 0, len(due))
	for _, a := range due {
		names = append(names, a.Name)
	}
	out.Data(map[string]any{"count": len(due), "due": names, "at": time.Now().UTC().Format(time.RFC3339)})
}

// agentTick runs everything that is due. This is what a timer calls: one
// entry point, however many agents, and no daemon holding state between them.
func agentTick(fl *flags) {
	h, cfg := hubFor(fl)
	due, err := h.DueAgents(time.Now().UTC())
	if err != nil {
		fail(err, "list due agents")
	}
	results := []map[string]any{}
	failed := 0
	for _, a := range due {
		run := executeAgent(h, cfg, fl, a, "schedule")
		if run.Status != "ok" {
			failed++
		}
		results = append(results, map[string]any{
			"agent": a.Name, "status": run.Status, "run": run.ID,
			"steps": run.Steps, "tokens": run.Tokens, "error": run.Error,
		})
	}
	fields := map[string]any{"ran": len(results), "failed": failed, "runs": results}
	if failed > 0 {
		// A timer that always exits 0 is a timer nobody looks at. One failed
		// agent makes the whole tick visible in `systemctl --failed`, while
		// the successful ones have already been recorded.
		b, _ := json.Marshal(fields)
		out.Fail(out.ExternalError, "agent_run_failed",
			pluralAgents(failed)+" failed", string(b))
	}
	out.Data(fields)
}

func agentRunOne(fl *flags) {
	h, cfg := hubFor(fl)
	a := resolveAgent(h, agentRef(fl))
	run := executeAgent(h, cfg, fl, a, "manual")
	fields := map[string]any{
		"run": run.ID, "agent": run.Agent, "status": run.Status,
		"steps": run.Steps, "tokens": run.Tokens, "thread": run.Thread,
		"answer": run.Answer, "tool_runs": run.ToolRuns,
		"scratchpad_written": run.Wrote, "duration_ms": run.Duration,
	}
	if run.Status != "ok" {
		b, _ := json.Marshal(fields)
		out.Fail(out.ExternalError, "agent_run_failed", run.Error, string(b))
	}
	out.Data(fields)
}

// executeAgent wires the provider, tools, memory and skills for one run.
func executeAgent(h *hub.Hub, cfg Config, fl *flags, a hub.Agent, trigger string) *hub.AgentRun {
	p, err := provider.New(cfg.Provider, provider.Config{
		BaseURL: cfg.ProviderURL, APIKey: cfg.ProviderKey, Timeout: cfg.providerTimeout(),
	})
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error())
	}
	opts := hub.RunAgentOptions{Agent: a, Provider: p, Trigger: trigger}

	// memgraph is needed only if this agent asked for tools, skills or a
	// memory project. An agent that asked for none must still run on a
	// machine that has no memgraph.
	if len(a.Tools) > 0 || len(a.Skills) > 0 || a.MemoryProject != "" {
		mc, err := memory.New(0)
		if err != nil {
			if len(a.Tools) > 0 {
				out.Fail(out.NotFound, "memgraph_missing", err.Error(),
					"this agent declares tools, and every tool is memgraph-backed")
			}
			out.Log("memory/skills: %v; running without them", err)
		} else {
			if a.MemoryProject != "" {
				opts.Memory = &hub.MemoryOptions{
					Client: mc, Project: a.MemoryProject, Limit: fl.intVal("memory-limit", 5),
				}
			}
			if len(a.Skills) > 0 {
				opts.Skills = &hub.SkillOptions{Client: mc}
			}
			if len(a.Tools) > 0 {
				reg, err := hub.NewRegistry(a.Tools, mc, a.MemoryProject, fl.has("allow-writes"))
				if err != nil {
					out.Fail(out.InvalidValue, "invalid_value", err.Error(), "ahub tool list")
				}
				opts.Registry = reg
			}
		}
	}
	run, err := h.RunAgent(context.Background(), opts)
	if err != nil {
		fail(err, "record agent run")
	}
	return run
}

func agentRuns(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListAgentRuns(fl.str("agent"), fl.str("status"), fl.intVal("limit", 20))
	if err != nil {
		fail(err, "list agent runs")
	}
	out.Data(map[string]any{"count": len(list), "runs": list})
}

// agentScratchpad reads or replaces the note an agent carries between runs.
// A human editing it is a supported operation: it is the one piece of an
// autonomous agent's state that is meant to be readable and correctable.
func agentScratchpad(fl *flags) {
	h, _ := hubFor(fl)
	a := resolveAgent(h, agentRef(fl))
	switch {
	case fl.has("clear"):
		if err := h.WriteScratchpad(a, ""); err != nil {
			fail(err, "clear scratchpad")
		}
		out.Data(map[string]any{"agent": a.Name, "cleared": true})
	case fl.str("set") != "":
		if err := h.WriteScratchpad(a, fl.str("set")); err != nil {
			fail(err, "write scratchpad")
		}
		out.Data(map[string]any{"agent": a.Name, "written": true})
	default:
		out.Data(map[string]any{
			"agent": a.Name, "scratchpad": a.Scratchpad,
			"chars": len(a.Scratchpad), "updated_at": a.ScratchpadAt,
		})
	}
}

func agentRef(fl *flags) string {
	if v := fl.str("agent"); v != "" {
		return v
	}
	return fl.arg(1)
}

func resolveAgent(h *hub.Hub, ref string) hub.Agent {
	if ref == "" {
		out.Fail(out.ValidationError, "validation_error", "name an agent", "ahub agent list")
	}
	a, err := h.FindAgent(ref)
	if err != nil {
		if !isBackend(err) {
			out.Fail(out.NotFound, "not_found", err.Error(), "ahub agent list")
		}
		fail(err, "resolve agent")
	}
	return a
}

func pluralAgents(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}
