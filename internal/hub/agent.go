package hub

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// ErrAmbiguous is returned when a name matches more than one agent.
var ErrAmbiguous = errors.New("name matches more than one agent")

// Agent is a saved configuration: the prompt, the model, and the knobs.
type Agent struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Model       string   `json:"model"`
	System      string   `json:"system,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	// MemoryProject names a memgraph scope to recall from before each turn.
	// Empty means the agent has no memory, which stays the default: an
	// agent that silently reads a knowledge store is one whose answers
	// cannot be reproduced from its own configuration.
	MemoryProject string `json:"memory_project,omitempty"`
	// Skills names procedures to load before each turn, resolved through
	// memgraph's skill graph. Unlike memories, a skill is instruction: it is
	// loaded only because someone named it here or on the command line.
	Skills []string `json:"skills,omitempty"`
	// Tools names what this agent may ask to have run. Empty means it
	// answers from what it was given, which stays the default: a tool loop
	// costs money and reaches outside the conversation.
	Tools     []string `json:"tools,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

func agentFrom(r bkn.Record) Agent {
	a := Agent{
		ID:            r.Str("id"),
		Name:          r.Str("name"),
		Model:         r.Str("model"),
		System:        r.Str("system"),
		Provider:      r.Str("provider"),
		MemoryProject: r.Str("memory_project"),
		CreatedAt:     r.Str("created_at"),
		UpdatedAt:     r.Str("updated_at"),
		MaxTokens:     int(r.Num("max_tokens")),
	}
	if v, ok := r["temperature"].(float64); ok {
		a.Temperature = &v
	}
	a.Skills = append(a.Skills, asList(r["skills"])...)
	a.Tools = append(a.Tools, asList(r["tools"])...)
	return a
}

// asList reads a JSON array of strings back out of a stored document.
func asList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// CreateAgent saves a new agent. Name and model are required: an
// agent without a model is a config that fails at the only moment it is
// ever used.
func (h *Hub) CreateAgent(a Agent) (Agent, error) {
	if strings.TrimSpace(a.Name) == "" {
		return Agent{}, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(a.Model) == "" {
		return Agent{}, fmt.Errorf("model is required")
	}
	doc := map[string]any{
		"name":       a.Name,
		"model":      a.Model,
		"created_at": now(),
		"updated_at": now(),
	}
	if a.System != "" {
		doc["system"] = a.System
	}
	if a.Provider != "" {
		doc["provider"] = a.Provider
	}
	if a.MemoryProject != "" {
		doc["memory_project"] = a.MemoryProject
	}
	if len(a.Skills) > 0 {
		doc["skills"] = a.Skills
	}
	if len(a.Tools) > 0 {
		doc["tools"] = a.Tools
	}
	if a.Temperature != nil {
		doc["temperature"] = *a.Temperature
	}
	if a.MaxTokens > 0 {
		doc["max_tokens"] = a.MaxTokens
	}
	rec, err := h.C.Create(RefAgents, doc)
	if err != nil {
		return Agent{}, err
	}
	return agentFrom(rec), nil
}

// ListAgents returns the caller's agents, newest first.
func (h *Hub) ListAgents(limit int) ([]Agent, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
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

// ResolveAgent finds an agent by id, then by exact name. Agents refer
// to agents by name; ids are what they store afterwards. An ambiguous
// name is an error rather than a silent first-match, because picking one
// quietly is how the wrong system prompt ends up in production.
func (h *Hub) ResolveAgent(ref string) (Agent, error) {
	if ref == "" {
		return Agent{}, fmt.Errorf("agent is required")
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
	switch len(recs) {
	case 0:
		return Agent{}, fmt.Errorf("no agent named %q", ref)
	case 1:
		return agentFrom(recs[0]), nil
	default:
		return Agent{}, fmt.Errorf("%w: %q matches %d", ErrAmbiguous, ref, len(recs))
	}
}

// UpdateAgent merges the given fields into an existing agent.
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

// DeleteAgent removes an agent. Threads that referenced it are left
// alone: a conversation that happened is a fact, and deleting the config that
// produced it does not un-happen it.
func (h *Hub) DeleteAgent(id string) error {
	return h.C.Delete(RefAgents, id)
}
