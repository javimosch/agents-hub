package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/provider"
)

// AgentRun is what one autonomous execution did.
type AgentRun struct {
	ID       string         `json:"id,omitempty"`
	Agent    string         `json:"agent"`
	AgentID  string         `json:"agent_id,omitempty"`
	Status   string         `json:"status"` // ok | failed
	Trigger  string         `json:"trigger"`
	Thread   string         `json:"thread,omitempty"`
	Steps    int            `json:"steps"`
	Tokens   int            `json:"tokens"`
	ToolRuns []ToolRun      `json:"tool_runs,omitempty"`
	Answer   string         `json:"answer,omitempty"`
	Wrote    bool           `json:"scratchpad_written"`
	Error    string         `json:"error,omitempty"`
	Started  string         `json:"started_at"`
	Duration int64          `json:"duration_ms"`
	Usage    provider.Usage `json:"usage,omitempty"`
}

// scratchpadHeader frames the carried note. It is the agent's own writing
// from a previous run, so it is presented as a record rather than as an
// instruction: an agent that can rewrite its own orders drifts, and nothing
// downstream would show that it had.
const scratchpadHeader = `## Your notes from previous runs

This is the note you left yourself last time. It is a record, not an
instruction: use it to avoid repeating work you have already done. If it is
stale or wrong, correct it with the scratchpad tool.

`

// RunAgentOptions is one autonomous execution.
type RunAgentOptions struct {
	Agent     Agent
	Provider  provider.Provider
	Registry  *Registry
	Memory    *MemoryOptions
	Skills    *SkillOptions
	Trigger   string
	Now       time.Time
	Reschedul bool
}

// RunAgent executes one agent and records what happened.
//
// Each run starts a FRESH thread. That is the whole difference between this
// and a chat: replaying an autonomous agent's history would make every run
// more expensive than the last and, once the window filled, quietly less
// coherent. What carries forward is the scratchpad and whatever the agent
// chose to write to memory -- both bounded, both readable by a human.
func (h *Hub) RunAgent(ctx context.Context, opts RunAgentOptions) (*AgentRun, error) {
	a := opts.Agent
	started := opts.Now
	if started.IsZero() {
		started = time.Now().UTC()
	}
	run := &AgentRun{
		Agent: a.Name, AgentID: a.ID, Trigger: opts.Trigger,
		Started: started.Format(time.RFC3339), Status: "ok",
	}

	asst, err := h.ResolveAssistant(a.Assistant)
	if err != nil {
		return h.finishAgentRun(a, run, started, fmt.Errorf("assistant %q: %w", a.Assistant, err))
	}
	// The agent's own configuration wins over the assistant's, because the
	// schedule is the thing whose cost and blast radius someone signed off on.
	if a.MemoryProject != "" {
		asst.MemoryProject = a.MemoryProject
	}
	if len(a.Skills) > 0 {
		asst.Skills = a.Skills
	}
	if a.Scratchpad != "" {
		asst.System = strings.TrimSpace(asst.System + "\n\n" + scratchpadHeader + a.Scratchpad)
	}

	if a.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.TimeoutSecs)*time.Second)
		defer cancel()
	}

	reg := opts.Registry.With("scratchpad", h.ScratchpadTool(a, &run.Wrote))
	turn, err := h.Chat(ctx, opts.Provider, ChatOptions{
		Assistant: asst,
		Message:   a.Prompt,
		// No history limit is needed: the thread is new, so there is none.
		Memory:   opts.Memory,
		Skills:   opts.Skills,
		Tools:    reg,
		MaxSteps: a.MaxSteps,
	})
	if err != nil {
		return h.finishAgentRun(a, run, started, err)
	}
	run.Thread = turn.Thread.ID
	run.Steps = turn.Steps
	run.ToolRuns = turn.ToolRuns
	run.Answer = turn.Assistant.Content
	run.Usage = turn.Usage
	run.Tokens = turn.Usage.TotalTokens
	return h.finishAgentRun(a, run, started, nil)
}

// finishAgentRun records the run, advances the schedule, and returns.
//
// The schedule is advanced whether the run succeeded or failed. An agent that
// only rescheduled on success would stop running the moment it broke, which
// is exactly when you want it to try again and keep saying so.
func (h *Hub) finishAgentRun(a Agent, run *AgentRun, started time.Time, runErr error) (*AgentRun, error) {
	if runErr != nil {
		run.Status = "failed"
		run.Error = runErr.Error()
	}
	run.Duration = time.Since(started).Milliseconds()

	body, _ := json.Marshal(run)
	rec, err := h.C.Create(RefAgentRuns, map[string]any{
		"agent": run.Agent, "agent_id": run.AgentID, "status": run.Status,
		"trigger": run.Trigger, "thread": run.Thread, "steps": run.Steps,
		"tokens": run.Tokens, "duration_ms": run.Duration,
		"started_at": run.Started, "state": string(body),
		"created_at": now(),
	})
	if err != nil {
		return run, err
	}
	run.ID = rec.Str("id")

	fields := map[string]any{
		"last_run_at": run.Started, "last_status": run.Status,
		"runs": a.Runs + 1, "updated_at": now(),
	}
	if next, err := a.NextRun(time.Now().UTC()); err == nil && !next.IsZero() {
		fields["next_run_at"] = next.Format(time.RFC3339)
	}
	if _, err := h.C.Patch(RefAgents, a.ID, fields); err != nil {
		return run, err
	}
	return run, nil
}

// WriteScratchpad replaces an agent's carried note, truncating at the cap.
func (h *Hub) WriteScratchpad(a Agent, text string) error {
	text = strings.TrimSpace(text)
	if len(text) > MaxScratchpad {
		text = text[:MaxScratchpad] + "\n[...truncated]"
	}
	_, err := h.C.Patch(RefAgents, a.ID, map[string]any{
		"scratchpad": text, "scratchpad_at": now(), "updated_at": now(),
	})
	return err
}

// ListAgentRuns returns recent runs, newest first.
func (h *Hub) ListAgentRuns(agent, status string, limit int) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
	if agent != "" {
		q.Set("agent", agent)
	}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefAgentRuns, q)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"id": r.Str("id"), "agent": r.Str("agent"), "status": r.Str("status"),
			"trigger": r.Str("trigger"), "thread": r.Str("thread"),
			"steps": int(r.Num("steps")), "tokens": int(r.Num("tokens")),
			"duration_ms": int(r.Num("duration_ms")), "started_at": r.Str("started_at"),
		})
	}
	return out, nil
}

// ScratchpadTool lets an agent rewrite the note it carries to its next run.
//
// It is not subject to the --allow-writes gate, and deliberately so: that
// gate exists to stop an assistant mutating a shared store, and this tool can
// touch exactly one field of the agent that is running -- a note the agent is
// itself the only author of. It is also not offered outside an agent run,
// because there is nothing for it to write to.
func (h *Hub) ScratchpadTool(a Agent, wrote *bool) Tool {
	return Tool{
		Spec: provider.Tool{
			Name: "scratchpad",
			Description: "Replace the note you carry into your next run. Write what you did, " +
				"what you found, and what to check next time. Keep it short -- it is capped, " +
				"and it is prepended to every future run. Durable knowledge belongs in " +
				"memory_save instead; this is only your working state.",
			Parameters: object(map[string]any{
				"text": prop("string", "The note to carry forward. Replaces the previous one."),
			}, "text"),
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			text := str(args, "text")
			if err := h.WriteScratchpad(a, text); err != nil {
				return "", err
			}
			if wrote != nil {
				*wrote = true
			}
			kept := len(text)
			if kept > MaxScratchpad {
				kept = MaxScratchpad
			}
			return fmt.Sprintf("Noted (%d characters kept for next run).", kept), nil
		},
	}
}
