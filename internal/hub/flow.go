package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/provider"
)

// Flow collections. A run is stored separately from its definition because a
// run outlives edits to the flow: what happened must stay readable even after
// the path it followed has been rewritten.
const (
	RefFlows    = NS + "/flows"
	RefFlowRuns = NS + "/flow_runs"
)

// SaveFlow stores a definition, replacing one of the same name.
func (h *Hub) SaveFlow(f *flow.Flow) (*flow.Flow, error) {
	body, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"name": f.Name, "description": f.Description,
		"definition": string(body), "updated_at": now(),
	}
	if existing, err := h.FindFlow(f.Name); err == nil {
		rec, err := h.C.Patch(RefFlows, existing.ID, doc)
		if err != nil {
			return nil, err
		}
		return flowFrom(rec)
	} else if !bkn.NotFound(err) {
		return nil, err
	}
	doc["created_at"] = now()
	rec, err := h.C.Create(RefFlows, doc)
	if err != nil {
		return nil, err
	}
	return flowFrom(rec)
}

// The definition is stored as a JSON string rather than a nested object for
// the same reason a tool call is: what has to survive is the document the
// author wrote, not a normalised version of it.
func flowFrom(r bkn.Record) (*flow.Flow, error) {
	f, err := flow.Parse([]byte(r.Str("definition")))
	if err != nil {
		return nil, fmt.Errorf("stored flow %q is not readable: %w", r.Str("name"), err)
	}
	f.ID = r.Str("id")
	f.CreatedAt, f.UpdatedAt = r.Str("created_at"), r.Str("updated_at")
	return f, nil
}

// FindFlow resolves a flow by id, then by name.
func (h *Hub) FindFlow(ref string) (*flow.Flow, error) {
	if ref == "" {
		return nil, fmt.Errorf("name a flow")
	}
	if rec, err := h.C.Get(RefFlows, ref); err == nil {
		return flowFrom(rec)
	} else if !bkn.NotFound(err) {
		return nil, err
	}
	q := url.Values{}
	q.Set("name", ref)
	recs, err := h.C.List(RefFlows, q)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, &bkn.APIError{Status: 404, Type: "not_found", Message: "no flow named " + ref}
	}
	return flowFrom(recs[0])
}

// ListFlows returns the caller's flows.
func (h *Hub) ListFlows(limit int) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("order_by", "updated_at")
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefFlows, q)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		f, err := flowFrom(r)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": f.ID, "name": f.Name, "description": f.Description,
			"steps": len(f.Steps), "updated_at": f.UpdatedAt,
		})
	}
	return out, nil
}

// DeleteFlow removes a definition. Its runs are left alone.
func (h *Hub) DeleteFlow(id string) error { return h.C.Delete(RefFlows, id) }

// SaveRun persists a run, creating it the first time.
func (h *Hub) SaveRun(r *flow.Run) (*flow.Run, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"flow": r.Flow, "flow_id": r.FlowID, "status": r.Status,
		"cursor": r.Cursor, "steps": r.Steps, "llm_calls": r.LLMCalls,
		"tokens": r.Tokens, "state": string(body), "updated_at": now(),
	}
	if r.ID == "" {
		doc["created_at"] = now()
		rec, err := h.C.Create(RefFlowRuns, doc)
		if err != nil {
			return nil, err
		}
		r.ID = rec.Str("id")
		return r, nil
	}
	if _, err := h.C.Patch(RefFlowRuns, r.ID, doc); err != nil {
		return nil, err
	}
	return r, nil
}

// GetRun loads a run.
func (h *Hub) GetRun(id string) (*flow.Run, error) {
	rec, err := h.C.Get(RefFlowRuns, id)
	if err != nil {
		return nil, err
	}
	var r flow.Run
	if err := json.Unmarshal([]byte(rec.Str("state")), &r); err != nil {
		return nil, fmt.Errorf("stored run %s is not readable: %w", id, err)
	}
	r.ID = rec.Str("id")
	return &r, nil
}

// ListRuns returns recent runs, newest first.
func (h *Hub) ListRuns(flowName, status string, limit int) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("order_by", "updated_at")
	if flowName != "" {
		q.Set("flow", flowName)
	}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefFlowRuns, q)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"id": r.Str("id"), "flow": r.Str("flow"), "status": r.Str("status"),
			"cursor": r.Str("cursor"), "steps": int(r.Num("steps")),
			"llm_calls": int(r.Num("llm_calls")), "tokens": int(r.Num("tokens")),
			"updated_at": r.Str("updated_at"),
		})
	}
	return out, nil
}

// Asker wires a flow's ask step to a real assistant and thread, so an LLM
// turn inside a flow is stored, auditable and resumable exactly like a chat.
// A flow that talks to a model leaves the same trail as a person who did.
func (h *Hub) Asker(p provider.Provider, defaults ChatOptions) flow.Asker {
	return func(ctx context.Context, assistant, prompt, thread string) (string, string, int, error) {
		a, err := h.ResolveAssistant(assistant)
		if err != nil {
			return "", thread, 0, err
		}
		opts := defaults
		opts.Assistant = a
		opts.Message = prompt
		opts.Thread = nil
		if thread != "" {
			t, err := h.GetThread(thread)
			if err != nil {
				return "", thread, 0, err
			}
			opts.Thread = &t
		}
		turn, err := h.Chat(ctx, p, opts)
		if err != nil {
			return "", thread, 0, err
		}
		return turn.Assistant.Content, turn.Thread.ID, turn.Usage.TotalTokens, nil
	}
}
