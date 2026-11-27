package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/javimosch/agents-hub/internal/eval"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/out"
	"github.com/javimosch/agents-hub/internal/provider"
)

// evalFailedExit is what --strict returns when assertions failed. Not a
// spec-band code, on purpose: the tool worked, the agent did not.
const evalFailedExit = 1

func evalCmd(fl *flags) {
	switch fl.arg(0) {
	case "validate":
		evalValidate(fl)
	case "create", "save", "import":
		evalSave(fl)
	case "", "list", "ls":
		evalList(fl)
	case "show", "get":
		evalShow(fl)
	case "rm", "delete":
		evalDelete(fl)
	case "run":
		evalRun(fl)
	case "runs":
		evalRuns(fl)
	case "compare", "diff":
		evalCompare(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"eval takes validate, create, list, show, rm, run, runs, compare")
	}
}

func readScenario(fl *flags) *eval.Scenario {
	path := fl.str("file")
	if path == "" {
		path = fl.arg(1)
	}
	var raw []byte
	var err error
	if path == "" || path == "-" {
		raw, err = readAllStdin()
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		out.Fail(out.ValidationError, "validation_error", err.Error(),
			"ahub eval validate --file quality.json")
	}
	s, perr := eval.Parse(raw)
	if perr != nil {
		out.Fail(out.ValidationError, "invalid_eval", perr.Error())
	}
	return s
}

// evalValidate is the cheap half: every regex compiles and every bound parses
// before a single token is spent finding out otherwise.
func evalValidate(fl *flags) {
	s := readScenario(fl)
	problems := eval.Validate(s)
	fields := map[string]any{
		"eval": s.Name, "agent": s.Agent, "cases": len(s.Cases),
		"problems": problems, "valid": !eval.Fatal(problems),
		"costly_assertions": countCostly(s),
	}
	if eval.Fatal(problems) {
		b, _ := json.Marshal(fields)
		out.Fail(out.ValidationError, "invalid_eval", string(b),
			"fix the fatal problems; warnings are safe to run with")
	}
	out.Data(fields)
}

func countCostly(s *eval.Scenario) int {
	n := 0
	for _, c := range s.Cases {
		for _, e := range c.Expect {
			if eval.Costly(e.Op) {
				n++
			}
		}
	}
	return n
}

func evalSave(fl *flags) {
	h, _ := hubFor(fl)
	if fl.has("from-thread") {
		evalFromThread(h, fl)
		return
	}
	s := readScenario(fl)
	if problems := eval.Validate(s); eval.Fatal(problems) {
		b, _ := json.Marshal(problems)
		out.Fail(out.ValidationError, "invalid_eval", string(b),
			"ahub eval validate --file <the same file>")
	}
	saved, err := h.SaveEval(s)
	if err != nil {
		fail(err, "save eval")
	}
	out.Data(map[string]any{"eval": map[string]any{
		"id": saved.ID, "name": saved.Name, "agent": saved.Agent, "cases": len(saved.Cases)}})
}

func evalList(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListEvals(fl.intVal("limit", 50))
	if err != nil {
		fail(err, "list evals")
	}
	out.Data(map[string]any{"count": len(list), "evals": list})
}

func evalShow(fl *flags) {
	h, _ := hubFor(fl)
	out.Data(map[string]any{"eval": resolveEval(h, evalRef(fl))})
}

func evalDelete(fl *flags) {
	h, _ := hubFor(fl)
	s := resolveEval(h, evalRef(fl))
	if err := h.DeleteEval(s.ID); err != nil {
		fail(err, "delete eval")
	}
	out.Data(map[string]any{"deleted": s.ID, "name": s.Name})
}

func evalRuns(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListEvalRuns(fl.str("eval"), fl.intVal("limit", 20))
	if err != nil {
		fail(err, "list eval runs")
	}
	out.Data(map[string]any{"count": len(list), "runs": list})
}

func evalRun(fl *flags) {
	h, cfg := hubFor(fl)
	s := resolveEval(h, evalRef(fl))

	agentName := fl.str("agent")
	if agentName == "" {
		agentName = s.Agent
	}
	agent := resolveAgent(h, agentName)
	for _, tool := range s.Tools {
		if !slices.Contains(agent.Tools, tool) {
			agent.Tools = append(agent.Tools, tool)
		}
	}

	opts := hub.EvalOptions{Scenario: s, Agent: agent, DailyBudget: cfg.DailyTokenBudget}
	if j := judgeName(fl, s); j != "" {
		judge := resolveAgent(h, j)
		opts.Judge = &judge
	}
	p, err := provider.New(cfg.Provider, provider.Config{
		BaseURL: cfg.ProviderURL, APIKey: cfg.ProviderKey, Timeout: cfg.providerTimeout(),
	})
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error())
	}
	opts.Provider = p
	attachAgentContext(fl, cfg, agent, &opts)

	// Progress on stderr: an eval can run for minutes and spend real money,
	// and a caller that learns nothing until the end cannot stop it early.
	opts.OnCase = func(r hub.CaseResult) {
		switch {
		case r.Skipped != "":
			out.Log("  skip %s (%s)", r.Case, r.Skipped)
		case r.Pass:
			out.Log("  pass %s (%d tokens)", r.Case, r.Tokens)
		default:
			out.Log("  FAIL %s (%d tokens) %s", r.Case, r.Tokens, firstFailure(r))
		}
	}

	run, err := h.RunEval(context.Background(), opts)
	if err != nil {
		fail(err, "run eval")
	}
	fields := map[string]any{
		"run": run.ID, "eval": run.Eval, "agent": run.Agent,
		"passed": run.Passed, "failed": run.Failed, "skipped": run.Skipped,
		"pass_rate": fmt.Sprintf("%.2f", run.Rate()),
		"tokens":    run.Tokens, "judge_tokens": run.Judge,
		"duration_ms": run.Duration, "cases": run.Cases,
	}
	// The exit code says whether the eval RAN, not whether it passed -- that
	// is data, and by default a completed eval exits 0 however it scored.
	// --strict flips it for a pipeline or a schedule that should go red on a
	// regression.
	//
	// It exits 1, deliberately outside cli-output-spec's 80-119 bands: those
	// classify TOOL failures, and every one of them would be a lie here. 105
	// in particular means "transient, retry", which would tell an agent to
	// run a failing eval again and expect a different answer.
	if fl.has("strict") && !run.Pass() {
		b, _ := json.Marshal(fields)
		out.Fail(evalFailedExit, "eval_failed",
			fmt.Sprintf("%d of %d cases failed", run.Failed, run.Passed+run.Failed), string(b))
	}
	out.Data(fields)
}

// attachAgentContext gives an eval run the same memory, skills and tools the
// agent would have in a chat. Measuring it without them would measure a
// different agent than the one that answers real questions.
func attachAgentContext(fl *flags, cfg Config, a hub.Agent, opts *hub.EvalOptions) {
	if len(a.Tools) == 0 && len(a.Skills) == 0 && a.MemoryProject == "" {
		return
	}
	var mc *memory.Client
	if c, err := memory.New(0); err == nil {
		mc = c
	} else if needsMemory(a.Tools) {
		out.Log("memory/skills/tools: %v; measuring without them", err)
		return
	} else if len(a.Skills) > 0 || a.MemoryProject != "" {
		out.Log("memory/skills: %v; measuring without them", err)
	}
	if mc != nil && a.MemoryProject != "" {
		opts.Memory = &hub.MemoryOptions{Client: mc, Project: a.MemoryProject, Limit: 5}
	}
	if mc != nil && len(a.Skills) > 0 {
		opts.Skills = &hub.SkillOptions{Client: mc}
	}
	if len(a.Tools) > 0 {
		reg, err := hub.NewRegistry(hub.RegistryOptions{
			Names: a.Tools, Memory: mc, Project: a.MemoryProject,
			AllowWrites: fl.has("allow-writes"),
			Policy:      cfg.toolPolicy(), Timeout: cfg.timeout(),
		})
		if err != nil {
			out.Fail(out.InvalidValue, "invalid_value", err.Error(), "ahub tool list")
		}
		opts.Registry = reg
	}
}

// evalCompare answers the only question that matters after a change.
func evalCompare(fl *flags) {
	h, _ := hubFor(fl)
	aID, bID := fl.arg(1), fl.arg(2)
	if aID == "" || bID == "" {
		out.Fail(out.ValidationError, "validation_error",
			"give two run ids: ahub eval compare <before> <after>", "ahub eval runs")
	}
	before, err := h.GetEvalRun(aID)
	if err != nil {
		fail(err, "load run")
	}
	after, err := h.GetEvalRun(bID)
	if err != nil {
		fail(err, "load run")
	}

	was := map[string]bool{}
	for _, c := range before.Cases {
		was[c.Case] = c.Pass
	}
	fixed, broke, still := []string{}, []string{}, []string{}
	for _, c := range after.Cases {
		prev, seen := was[c.Case]
		switch {
		case !seen:
			continue
		case c.Pass && !prev:
			fixed = append(fixed, c.Case)
		case !c.Pass && prev:
			broke = append(broke, c.Case)
		case !c.Pass && !prev:
			still = append(still, c.Case)
		}
	}
	out.Data(map[string]any{
		"before": map[string]any{"run": before.ID, "agent": before.Agent,
			"passed": before.Passed, "failed": before.Failed, "tokens": before.Tokens},
		"after": map[string]any{"run": after.ID, "agent": after.Agent,
			"passed": after.Passed, "failed": after.Failed, "tokens": after.Tokens},
		"fixed": fixed, "regressed": broke, "still_failing": still,
		"token_delta": after.Tokens - before.Tokens,
		// A change that fixes two cases and breaks one is not obviously an
		// improvement; the caller is told both numbers rather than a verdict.
		"summary": fmt.Sprintf("%d fixed, %d regressed, %d still failing",
			len(fixed), len(broke), len(still)),
	})
}

func firstFailure(r hub.CaseResult) string {
	if r.Error != "" {
		return r.Error
	}
	for _, c := range r.Checks {
		if !c.Pass {
			if c.Detail != "" {
				return c.Op + ": " + c.Detail
			}
			return c.Op
		}
	}
	return ""
}

func judgeName(fl *flags, s *eval.Scenario) string {
	if v := fl.str("judge"); v != "" {
		return v
	}
	return s.Judge
}

func evalRef(fl *flags) string {
	if v := fl.str("eval"); v != "" {
		return v
	}
	return fl.arg(1)
}

func resolveEval(h *hub.Hub, ref string) *eval.Scenario {
	if ref == "" {
		out.Fail(out.ValidationError, "validation_error", "name an eval", "ahub eval list")
	}
	s, err := h.FindEval(ref)
	if err != nil {
		if !isBackend(err) {
			out.Fail(out.NotFound, "not_found", err.Error(), "ahub eval list")
		}
		fail(err, "resolve eval")
	}
	return s
}
