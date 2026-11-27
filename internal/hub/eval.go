package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/eval"
	"github.com/javimosch/agents-hub/internal/provider"
)

// Eval collections.
const (
	RefEvals    = NS + "/evals"
	RefEvalRuns = NS + "/eval_runs"
)

// CaseResult is one case's outcome and verdicts.
type CaseResult struct {
	Case    string        `json:"case"`
	Pass    bool          `json:"pass"`
	Skipped string        `json:"skipped,omitempty"`
	Answer  string        `json:"answer,omitempty"`
	Thread  string        `json:"thread,omitempty"`
	Steps   int           `json:"steps"`
	Tokens  int           `json:"tokens"`
	Tools   []string      `json:"tools,omitempty"`
	Checks  []eval.Result `json:"checks,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// EvalRun is one execution of a scenario.
type EvalRun struct {
	ID       string       `json:"id,omitempty"`
	Eval     string       `json:"eval"`
	Agent    string       `json:"agent"`
	Passed   int          `json:"passed"`
	Failed   int          `json:"failed"`
	Skipped  int          `json:"skipped"`
	Tokens   int          `json:"tokens"`
	Judge    int          `json:"judge_tokens"`
	Duration int64        `json:"duration_ms"`
	Cases    []CaseResult `json:"cases"`
	Started  string       `json:"started_at"`
}

// Pass reports whether every case that ran passed.
func (r EvalRun) Pass() bool { return r.Failed == 0 }

// Rate is the pass rate over the cases that actually ran.
func (r EvalRun) Rate() float64 {
	ran := r.Passed + r.Failed
	if ran == 0 {
		return 0
	}
	return float64(r.Passed) / float64(ran)
}

// SaveEval stores a scenario, replacing one of the same name.
func (h *Hub) SaveEval(s *eval.Scenario) (*eval.Scenario, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"name": s.Name, "agent": s.Agent, "description": s.Description,
		"cases": len(s.Cases), "definition": string(body), "updated_at": now(),
	}
	if existing, err := h.FindEval(s.Name); err == nil {
		rec, err := h.C.Patch(RefEvals, existing.ID, doc)
		if err != nil {
			return nil, err
		}
		return evalFrom(rec)
	} else if !bkn.NotFound(err) {
		return nil, err
	}
	doc["created_at"] = now()
	rec, err := h.C.Create(RefEvals, doc)
	if err != nil {
		return nil, err
	}
	return evalFrom(rec)
}

func evalFrom(r bkn.Record) (*eval.Scenario, error) {
	s, err := eval.Parse([]byte(r.Str("definition")))
	if err != nil {
		return nil, fmt.Errorf("stored eval %q is not readable: %w", r.Str("name"), err)
	}
	s.ID = r.Str("id")
	s.CreatedAt, s.UpdatedAt = r.Str("created_at"), r.Str("updated_at")
	return s, nil
}

// FindEval resolves by id then by name.
func (h *Hub) FindEval(ref string) (*eval.Scenario, error) {
	if ref == "" {
		return nil, fmt.Errorf("name an eval")
	}
	if rec, err := h.C.Get(RefEvals, ref); err == nil {
		return evalFrom(rec)
	} else if !bkn.NotFound(err) {
		return nil, err
	}
	q := url.Values{}
	q.Set("name", ref)
	recs, err := h.C.List(RefEvals, q)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, &bkn.APIError{Status: 404, Type: "not_found", Message: "no eval named " + ref}
	}
	return evalFrom(recs[0])
}

// ListEvals returns the caller's scenarios.
func (h *Hub) ListEvals(limit int) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("order_by", "name")
	q.Set("order", "asc")
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefEvals, q)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"id": r.Str("id"), "name": r.Str("name"), "agent": r.Str("agent"),
			"cases": int(r.Num("cases")), "updated_at": r.Str("updated_at"),
		})
	}
	return out, nil
}

// DeleteEval removes a scenario, leaving its runs: what a run measured is a
// fact, and deleting the scenario does not un-measure it.
func (h *Hub) DeleteEval(id string) error { return h.C.Delete(RefEvals, id) }

// EvalOptions is one execution of a scenario.
type EvalOptions struct {
	Scenario *eval.Scenario
	Agent    Agent
	Judge    *Agent
	Provider provider.Provider
	Registry *Registry
	Memory   *MemoryOptions
	Skills   *SkillOptions
	// OnCase reports progress as it happens. An eval can take minutes and
	// cost real money; a caller that learns nothing until the end cannot stop
	// it when the first three cases already show the answer.
	OnCase func(CaseResult)
	// DailyBudget stops a scenario part-way rather than spending past the
	// day's allowance. An eval is the easiest way to spend a lot quickly.
	DailyBudget int
}

// RunEval executes every case and scores it.
//
// Each case gets a FRESH thread, for the same reason a schedule run does: a
// case that inherited the previous case's conversation would be measuring the
// order of the file as much as the agent.
func (h *Hub) RunEval(ctx context.Context, opts EvalOptions) (*EvalRun, error) {
	started := time.Now()
	run := &EvalRun{
		Eval: opts.Scenario.Name, Agent: opts.Agent.Name,
		Started: started.UTC().Format(time.RFC3339),
	}
	judge := h.judgeFor(ctx, opts, run)

	for _, c := range opts.Scenario.Cases {
		res := CaseResult{Case: c.Name}
		if c.Skip != "" {
			res.Skipped = c.Skip
			run.Skipped++
			run.Cases = append(run.Cases, res)
			if opts.OnCase != nil {
				opts.OnCase(res)
			}
			continue
		}
		turn, err := h.Chat(ctx, opts.Provider, ChatOptions{
			Agent: opts.Agent, Message: c.Message,
			Memory: opts.Memory, Skills: opts.Skills, Tools: opts.Registry,
			DailyBudget: opts.DailyBudget,
		})
		if err != nil {
			// A case that could not run is a failure, not a skip. Counting it
			// as a pass would make an outage look like an improvement.
			res.Error = err.Error()
			run.Failed++
			run.Cases = append(run.Cases, res)
			if opts.OnCase != nil {
				opts.OnCase(res)
			}
			continue
		}
		res.Answer = turn.Reply.Content
		res.Thread = turn.Thread.ID
		res.Steps = turn.Steps
		res.Tokens = turn.Usage.TotalTokens
		for _, tr := range turn.ToolRuns {
			res.Tools = append(res.Tools, tr.Name)
		}
		run.Tokens += res.Tokens

		outcome := eval.Outcome{
			Answer: res.Answer, Tokens: res.Tokens, Steps: res.Steps, Tools: res.Tools,
		}
		res.Pass = true
		for _, e := range c.Expect {
			r := eval.Check(e, outcome, judge)
			run.Judge += r.Tokens
			run.Tokens += r.Tokens
			if !r.Pass {
				res.Pass = false
			}
			res.Checks = append(res.Checks, r)
		}
		if res.Pass {
			run.Passed++
		} else {
			run.Failed++
		}
		run.Cases = append(run.Cases, res)
		if opts.OnCase != nil {
			opts.OnCase(res)
		}
	}
	run.Duration = time.Since(started).Milliseconds()
	return h.saveEvalRun(run)
}

// judgeFor builds the judge callback, or nil when the scenario has no judge.
func (h *Hub) judgeFor(ctx context.Context, opts EvalOptions, run *EvalRun) eval.Judge {
	if opts.Judge == nil {
		return nil
	}
	j := *opts.Judge
	return func(question, answer string) (bool, string, int, error) {
		// The judge is asked for a verdict token first and its reasoning
		// after, so a truncated or rambling reply still parses.
		prompt := "Answer with PASS or FAIL on the first line, then one short sentence of reasoning.\n\n" +
			"Question: " + question + "\n\nAnswer under test:\n" + answer
		turn, err := h.Chat(ctx, opts.Provider, ChatOptions{
			Agent: j, Message: prompt, DailyBudget: opts.DailyBudget})
		if err != nil {
			return false, "", 0, err
		}
		text := strings.TrimSpace(turn.Reply.Content)
		verdict := strings.ToUpper(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]))
		pass := strings.HasPrefix(verdict, "PASS")
		reason := strings.TrimSpace(strings.TrimPrefix(text, strings.SplitN(text, "\n", 2)[0]))
		if reason == "" {
			reason = text
		}
		return pass, reason, turn.Usage.TotalTokens, nil
	}
}

func (h *Hub) saveEvalRun(run *EvalRun) (*EvalRun, error) {
	body, err := json.Marshal(run)
	if err != nil {
		return run, err
	}
	rec, err := h.C.Create(RefEvalRuns, map[string]any{
		"eval": run.Eval, "agent": run.Agent,
		"passed": run.Passed, "failed": run.Failed, "skipped": run.Skipped,
		"tokens": run.Tokens, "judge_tokens": run.Judge,
		"duration_ms": run.Duration, "started_at": run.Started,
		"state": string(body), "created_at": now(),
	})
	if err != nil {
		return run, err
	}
	run.ID = rec.Str("id")
	return run, nil
}

// GetEvalRun loads a stored run.
func (h *Hub) GetEvalRun(id string) (*EvalRun, error) {
	rec, err := h.C.Get(RefEvalRuns, id)
	if err != nil {
		return nil, err
	}
	var run EvalRun
	if err := json.Unmarshal([]byte(rec.Str("state")), &run); err != nil {
		return nil, fmt.Errorf("stored eval run %s is not readable: %w", id, err)
	}
	run.ID = rec.Str("id")
	return &run, nil
}

// ListEvalRuns returns recent runs, newest first.
func (h *Hub) ListEvalRuns(name string, limit int) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
	if name != "" {
		q.Set("eval", name)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefEvalRuns, q)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		passed, failed := int(r.Num("passed")), int(r.Num("failed"))
		out = append(out, map[string]any{
			"id": r.Str("id"), "eval": r.Str("eval"), "agent": r.Str("agent"),
			"passed": passed, "failed": failed, "skipped": int(r.Num("skipped")),
			"tokens": int(r.Num("tokens")), "judge_tokens": int(r.Num("judge_tokens")),
			"duration_ms": int(r.Num("duration_ms")), "started_at": r.Str("started_at"),
		})
	}
	return out, nil
}
