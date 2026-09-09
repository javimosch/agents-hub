package hub

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// Agent collections.
const (
	RefAgents    = NS + "/agents"
	RefAgentRuns = NS + "/agent_runs"
)

// Agent is a standing instruction: run this assistant, on this schedule, with
// no user in the loop.
//
// The interesting field is Scratchpad. An autonomous agent that replayed its
// whole conversation history would get more expensive every run and, once the
// window filled, quietly less coherent. Instead each run starts a fresh
// thread and carries forward one bounded note that the agent itself writes.
// Knowledge that deserves to outlive the agent goes to memgraph through the
// memory_save tool; the scratchpad is only "where I got to last time".
type Agent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Assistant string `json:"assistant"`
	Prompt    string `json:"prompt"`

	// Schedule. EverySecs and At are the whole vocabulary: anything more
	// elaborate is a systemd timer calling `ahub agent run <name>`, which is
	// a scheduler that already exists and that nobody has to debug here.
	EverySecs int    `json:"every_secs,omitempty"`
	At        string `json:"at,omitempty"` // HH:MM, daily
	Enabled   bool   `json:"enabled"`

	Tools         []string `json:"tools,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	MemoryProject string   `json:"memory_project,omitempty"`
	MaxSteps      int      `json:"max_steps,omitempty"`
	TimeoutSecs   int      `json:"timeout_secs,omitempty"`

	Scratchpad   string `json:"scratchpad,omitempty"`
	ScratchpadAt string `json:"scratchpad_at,omitempty"`

	LastRunAt  string `json:"last_run_at,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	NextRunAt  string `json:"next_run_at,omitempty"`
	Runs       int    `json:"runs"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// MaxScratchpad bounds what one run may carry to the next. The whole point of
// a scratchpad over history is that it cannot grow without limit; a cap that
// is never enforced is a cap that does not exist.
const MaxScratchpad = 4000

func agentFrom(r bkn.Record) Agent {
	a := Agent{
		ID: r.Str("id"), Name: r.Str("name"), Assistant: r.Str("assistant"),
		Prompt: r.Str("prompt"), At: r.Str("at"),
		EverySecs:     int(r.Num("every_secs")),
		Enabled:       r["enabled"] != false,
		MemoryProject: r.Str("memory_project"),
		MaxSteps:      int(r.Num("max_steps")),
		TimeoutSecs:   int(r.Num("timeout_secs")),
		Scratchpad:    r.Str("scratchpad"), ScratchpadAt: r.Str("scratchpad_at"),
		LastRunAt: r.Str("last_run_at"), LastStatus: r.Str("last_status"),
		NextRunAt: r.Str("next_run_at"), Runs: int(r.Num("runs")),
		CreatedAt: r.Str("created_at"), UpdatedAt: r.Str("updated_at"),
	}
	a.Tools = append(a.Tools, asList(r["tools"])...)
	a.Skills = append(a.Skills, asList(r["skills"])...)
	return a
}

// NextRun computes when an agent should run after a given moment.
//
// It is computed forward from now rather than from the previous due time, so
// a machine that was off for a week runs once when it comes back instead of
// trying to catch up on every missed slot at once.
func (a Agent) NextRun(from time.Time) (time.Time, error) {
	switch {
	case a.EverySecs > 0:
		return from.Add(time.Duration(a.EverySecs) * time.Second), nil
	case a.At != "":
		hh, mm, err := parseAt(a.At)
		if err != nil {
			return time.Time{}, err
		}
		next := time.Date(from.Year(), from.Month(), from.Day(), hh, mm, 0, 0, from.Location())
		if !next.After(from) {
			next = next.Add(24 * time.Hour)
		}
		return next, nil
	}
	// No schedule: the agent only ever runs when asked. That is a legitimate
	// configuration -- a webhook or a human is the trigger -- so it is not an
	// error, it simply never becomes due.
	return time.Time{}, nil
}

// ValidateAt lets the CLI reject a malformed time of day as an invalid value
// (85) before the domain reports it as a validation error (82). Both layers
// check: the CLI so the exit code matches every other bad flag value, the
// domain so a caller that bypasses the CLI cannot store an unschedulable
// agent.
func ValidateAt(s string) error {
	if s == "" {
		return nil
	}
	_, _, err := parseAt(s)
	return err
}

func parseAt(s string) (int, int, error) {
	var hh, mm int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &hh, &mm); err != nil {
		return 0, 0, fmt.Errorf("--at takes HH:MM, got %q", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("--at %q is not a valid time of day", s)
	}
	return hh, mm, nil
}

// CreateAgent stores a new schedule and computes its first due time.
func (h *Hub) CreateAgent(a Agent) (Agent, error) {
	if strings.TrimSpace(a.Name) == "" {
		return Agent{}, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(a.Assistant) == "" {
		return Agent{}, fmt.Errorf("assistant is required")
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return Agent{}, fmt.Errorf("prompt is required: an agent with nothing to do is a schedule that burns tokens")
	}
	if a.EverySecs > 0 && a.At != "" {
		return Agent{}, fmt.Errorf("give --every or --at, not both")
	}
	next, err := a.NextRun(time.Now().UTC())
	if err != nil {
		return Agent{}, err
	}
	doc := map[string]any{
		"name": a.Name, "assistant": a.Assistant, "prompt": a.Prompt,
		"enabled": true, "runs": 0,
		"created_at": now(), "updated_at": now(),
	}
	if a.EverySecs > 0 {
		doc["every_secs"] = a.EverySecs
	}
	if a.At != "" {
		doc["at"] = a.At
	}
	if !next.IsZero() {
		doc["next_run_at"] = next.Format(time.RFC3339)
	}
	for k, v := range map[string]any{
		"tools": a.Tools, "skills": a.Skills,
		"memory_project": a.MemoryProject, "max_steps": a.MaxSteps,
		"timeout_secs": a.TimeoutSecs,
	} {
		switch t := v.(type) {
		case []string:
			if len(t) > 0 {
				doc[k] = t
			}
		case string:
			if t != "" {
				doc[k] = t
			}
		case int:
			if t > 0 {
				doc[k] = t
			}
		}
	}
	rec, err := h.C.Create(RefAgents, doc)
	if err != nil {
		return Agent{}, err
	}
	return agentFrom(rec), nil
}

// FindAgent resolves by id then by name.
func (h *Hub) FindAgent(ref string) (Agent, error) {
	if ref == "" {
		return Agent{}, fmt.Errorf("name an agent")
	}
	if rec, err := h.C.Get(RefAgents, ref); err == nil {
		return agentFrom(rec), nil
	} else if !bkn.NotFound(err) {
		return Agent{}, err
	}
	q := url.Values{}
	q.Set("name", ref)
	recs, err := h.C.List(RefAgents, q)
	if err != nil {
		return Agent{}, err
	}
	if len(recs) == 0 {
		return Agent{}, &bkn.APIError{Status: 404, Type: "not_found", Message: "no agent named " + ref}
	}
	return agentFrom(recs[0]), nil
}

// ListAgents returns the caller's agents.
func (h *Hub) ListAgents(limit int) ([]Agent, error) {
	q := url.Values{}
	q.Set("order_by", "name")
	q.Set("order", "asc")
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefAgents, q)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(recs))
	for _, r := range recs {
		out = append(out, agentFrom(r))
	}
	return out, nil
}

// DueAgents returns the enabled agents whose next run has arrived.
func (h *Hub) DueAgents(at time.Time) ([]Agent, error) {
	all, err := h.ListAgents(0)
	if err != nil {
		return nil, err
	}
	due := []Agent{}
	for _, a := range all {
		if !a.Enabled || a.NextRunAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, a.NextRunAt)
		if err != nil || t.After(at) {
			continue
		}
		due = append(due, a)
	}
	return due, nil
}

// UpdateAgent merges fields into an agent.
func (h *Hub) UpdateAgent(id string, fields map[string]any) (Agent, error) {
	if len(fields) == 0 {
		return Agent{}, fmt.Errorf("give at least one field to change")
	}
	fields["updated_at"] = now()
	rec, err := h.C.Patch(RefAgents, id, fields)
	if err != nil {
		return Agent{}, err
	}
	return agentFrom(rec), nil
}

// DeleteAgent removes a schedule. Its run history is left alone: what an
// agent did is a fact, and deleting the schedule does not un-run it.
func (h *Hub) DeleteAgent(id string) error { return h.C.Delete(RefAgents, id) }
