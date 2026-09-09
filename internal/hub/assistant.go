package hub

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// ErrAmbiguous is returned when a name matches more than one assistant.
var ErrAmbiguous = errors.New("name matches more than one assistant")

// Assistant is a saved configuration: the prompt, the model, and the knobs.
type Assistant struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Model       string   `json:"model"`
	System      string   `json:"system,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

func assistantFrom(r bkn.Record) Assistant {
	a := Assistant{
		ID:        r.Str("id"),
		Name:      r.Str("name"),
		Model:     r.Str("model"),
		System:    r.Str("system"),
		Provider:  r.Str("provider"),
		CreatedAt: r.Str("created_at"),
		UpdatedAt: r.Str("updated_at"),
		MaxTokens: int(r.Num("max_tokens")),
	}
	if v, ok := r["temperature"].(float64); ok {
		a.Temperature = &v
	}
	return a
}

// CreateAssistant saves a new assistant. Name and model are required: an
// assistant without a model is a config that fails at the only moment it is
// ever used.
func (h *Hub) CreateAssistant(a Assistant) (Assistant, error) {
	if strings.TrimSpace(a.Name) == "" {
		return Assistant{}, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(a.Model) == "" {
		return Assistant{}, fmt.Errorf("model is required")
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
	if a.Temperature != nil {
		doc["temperature"] = *a.Temperature
	}
	if a.MaxTokens > 0 {
		doc["max_tokens"] = a.MaxTokens
	}
	rec, err := h.C.Create(RefAssistants, doc)
	if err != nil {
		return Assistant{}, err
	}
	return assistantFrom(rec), nil
}

// ListAssistants returns the caller's assistants, newest first.
func (h *Hub) ListAssistants(limit int) ([]Assistant, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefAssistants, q)
	if err != nil {
		return nil, err
	}
	out := make([]Assistant, 0, len(recs))
	for _, r := range recs {
		out = append(out, assistantFrom(r))
	}
	return out, nil
}

// ResolveAssistant finds an assistant by id, then by exact name. Agents refer
// to assistants by name; ids are what they store afterwards. An ambiguous
// name is an error rather than a silent first-match, because picking one
// quietly is how the wrong system prompt ends up in production.
func (h *Hub) ResolveAssistant(ref string) (Assistant, error) {
	if ref == "" {
		return Assistant{}, fmt.Errorf("assistant is required")
	}
	if rec, err := h.C.Get(RefAssistants, ref); err == nil {
		return assistantFrom(rec), nil
	} else if !bkn.NotFound(err) {
		return Assistant{}, err
	}
	q := url.Values{}
	q.Set("name", ref)
	recs, err := h.C.List(RefAssistants, q)
	if err != nil {
		return Assistant{}, err
	}
	switch len(recs) {
	case 0:
		return Assistant{}, fmt.Errorf("no assistant named %q", ref)
	case 1:
		return assistantFrom(recs[0]), nil
	default:
		return Assistant{}, fmt.Errorf("%w: %q matches %d", ErrAmbiguous, ref, len(recs))
	}
}

// UpdateAssistant merges the given fields into an existing assistant.
func (h *Hub) UpdateAssistant(id string, fields map[string]any) (Assistant, error) {
	if len(fields) == 0 {
		return Assistant{}, fmt.Errorf("give at least one field to change")
	}
	fields["updated_at"] = now()
	rec, err := h.C.Patch(RefAssistants, id, fields)
	if err != nil {
		return Assistant{}, err
	}
	return assistantFrom(rec), nil
}

// DeleteAssistant removes an assistant. Threads that referenced it are left
// alone: a conversation that happened is a fact, and deleting the config that
// produced it does not un-happen it.
func (h *Hub) DeleteAssistant(id string) error {
	return h.C.Delete(RefAssistants, id)
}
