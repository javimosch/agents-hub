package hub

import (
	"fmt"
	"net/url"
)

// RefModels holds what has been learned about the models themselves.
//
// A context window is a fact about a model, not a policy of this machine --
// so unlike an allowlist or a credential, it belongs where every machine can
// see it. Without this each one rediscovers the same number the same way: by
// paying for a call the provider refuses.
const RefModels = NS + "/models"

// ModelFact is what is known about one model.
type ModelFact struct {
	Model     string `json:"model"`
	Limit     int    `json:"context_limit"`
	Source    string `json:"source,omitempty"` // learned | declared
	LearnedAt string `json:"learned_at,omitempty"`
}

// LoadModelLimits returns every context window known to this account.
func (h *Hub) LoadModelLimits() (map[string]int, error) {
	q := url.Values{}
	q.Set("limit", "200")
	recs, err := h.C.List(RefModels, q)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(recs))
	for _, r := range recs {
		if m, n := r.Str("model"), int(r.Num("context_limit")); m != "" && n > 0 {
			out[m] = n
		}
	}
	return out, nil
}

// SaveModelLimit records a window, replacing any earlier belief about it.
//
// Later beliefs win rather than being merged: a provider that has just
// refused a prompt is a better source than a number remembered from before,
// and two entries for one model would leave the next reader guessing.
func (h *Hub) SaveModelLimit(model string, limit int, source string) error {
	if model == "" || limit <= 0 {
		return fmt.Errorf("a model limit needs a model and a positive size")
	}
	doc := map[string]any{
		"model": model, "context_limit": limit, "source": source,
		"learned_at": now(), "updated_at": now(),
	}
	q := url.Values{}
	q.Set("model", model)
	recs, err := h.C.List(RefModels, q)
	if err != nil {
		return err
	}
	if len(recs) > 0 {
		_, err := h.C.Patch(RefModels, recs[0].Str("id"), doc)
		return err
	}
	doc["created_at"] = now()
	_, err = h.C.Create(RefModels, doc)
	return err
}

// ListModelFacts returns what is known, newest first.
func (h *Hub) ListModelFacts(limit int) ([]ModelFact, error) {
	q := url.Values{}
	q.Set("order_by", "learned_at")
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefModels, q)
	if err != nil {
		return nil, err
	}
	out := make([]ModelFact, 0, len(recs))
	for _, r := range recs {
		out = append(out, ModelFact{
			Model: r.Str("model"), Limit: int(r.Num("context_limit")),
			Source: r.Str("source"), LearnedAt: r.Str("learned_at"),
		})
	}
	return out, nil
}
