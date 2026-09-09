package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
	"github.com/javimosch/agents-hub/internal/provider"
)

func flowCmd(fl *flags) {
	switch fl.arg(0) {
	case "validate":
		flowValidate(fl)
	case "create", "save", "import":
		flowSave(fl)
	case "", "list", "ls":
		flowList(fl)
	case "show", "get":
		flowShow(fl)
	case "rm", "delete":
		flowDelete(fl)
	case "run":
		flowRun(fl, "")
	case "resume":
		flowRun(fl, fl.arg(1))
	case "runs":
		flowRuns(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"flow takes validate, create, list, show, rm, run, resume, runs")
	}
}

// readDefinition loads a flow from --file or stdin.
func readDefinition(fl *flags) []byte {
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
			"ahub flow validate --file intake.json")
	}
	return raw
}

func readAllStdin() ([]byte, error) {
	st, err := os.Stdin.Stat()
	if err == nil && st.Mode()&os.ModeCharDevice != 0 {
		// Reading from a terminal would block a tool that must never wait for
		// a human. Say so instead.
		return nil, errNoInput
	}
	return readAll(os.Stdin)
}

// flowValidate is the cheapest command here and the most useful: because the
// path is data, every one of these problems is findable before a run spends
// anything.
func flowValidate(fl *flags) {
	f, err := flow.Parse(readDefinition(fl))
	if err != nil {
		out.Fail(out.ValidationError, "invalid_flow", err.Error())
	}
	cfg, _, cerr := resolve(fl)
	if cerr != nil {
		out.Fail(out.ValidationError, "config_error", cerr.Error())
	}
	problems := flow.Validate(f, cfg.AllowCmds, cfg.AllowHosts)
	fields := map[string]any{
		"flow": f.Name, "steps": len(f.Steps),
		"problems": problems, "valid": !flow.Fatal(problems),
	}
	if flow.Fatal(problems) {
		b, _ := json.Marshal(fields)
		out.Fail(out.ValidationError, "invalid_flow", string(b),
			"fix the fatal problems; warnings are safe to run with")
	}
	out.Data(fields)
}

func flowSave(fl *flags) {
	h, _ := hubFor(fl)
	f, err := flow.Parse(readDefinition(fl))
	if err != nil {
		out.Fail(out.ValidationError, "invalid_flow", err.Error())
	}
	cfg, _, _ := resolve(fl)
	if problems := flow.Validate(f, cfg.AllowCmds, cfg.AllowHosts); flow.Fatal(problems) {
		b, _ := json.Marshal(problems)
		out.Fail(out.ValidationError, "invalid_flow", string(b),
			"ahub flow validate --file <the same file>")
	}
	saved, err := h.SaveFlow(f)
	if err != nil {
		fail(err, "save flow")
	}
	out.Data(map[string]any{"flow": map[string]any{
		"id": saved.ID, "name": saved.Name, "steps": len(saved.Steps)}})
}

func flowList(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListFlows(fl.intVal("limit", 50))
	if err != nil {
		fail(err, "list flows")
	}
	out.Data(map[string]any{"count": len(list), "flows": list})
}

func flowShow(fl *flags) {
	h, _ := hubFor(fl)
	f, err := h.FindFlow(flowRef(fl))
	if err != nil {
		fail(err, "load flow")
	}
	out.Data(map[string]any{"flow": f})
}

func flowDelete(fl *flags) {
	h, _ := hubFor(fl)
	f, err := h.FindFlow(flowRef(fl))
	if err != nil {
		fail(err, "load flow")
	}
	if err := h.DeleteFlow(f.ID); err != nil {
		fail(err, "delete flow")
	}
	out.Data(map[string]any{"deleted": f.ID, "name": f.Name})
}

func flowRuns(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListRuns(fl.str("flow"), fl.str("status"), fl.intVal("limit", 20))
	if err != nil {
		fail(err, "list runs")
	}
	out.Data(map[string]any{"count": len(list), "runs": list})
}

// flowRun starts a run, or resumes one when given an id.
func flowRun(fl *flags, resumeID string) {
	h, cfg := hubFor(fl)
	inputs := inputPairs(fl)

	var run *flow.Run
	var f *flow.Flow
	var err error
	if resumeID != "" {
		run, err = h.GetRun(resumeID)
		if err != nil {
			fail(err, "load run")
		}
		if run.Status == flow.StatusCompleted || run.Status == flow.StatusFailed {
			out.Fail(out.Conflict, "run_finished",
				"run "+resumeID+" already "+run.Status,
				"ahub flow run "+run.Flow+" to start a new one")
		}
		f, err = h.FindFlow(run.FlowID)
		if err != nil {
			f, err = h.FindFlow(run.Flow)
		}
	} else {
		f, err = h.FindFlow(flowRef(fl))
	}
	if err != nil {
		fail(err, "load flow")
	}

	engine := &flow.Engine{
		Flow:     f,
		Secrets:  cfg.Secrets,
		MaxSteps: fl.intVal("max-steps", 0),
		Limits: flow.Limits{
			Commands: cfg.AllowCmds,
			Hosts:    cfg.AllowHosts,
			Timeout:  time.Duration(fl.intVal("step-timeout", 60)) * time.Second,
		},
	}
	// bkn is already reachable -- the flow itself came from there -- so the
	// script bridge costs nothing to attach.
	engine.Script = h.Scripter()

	// The model is wired in only when the flow can actually reach one, so a
	// purely deterministic flow never touches the provider layer at all.
	if usesAsk(f) {
		p, perr := provider.New(cfg.Provider, provider.Config{
			BaseURL: cfg.ProviderURL, APIKey: cfg.ProviderKey, Timeout: cfg.providerTimeout(),
		})
		if perr != nil {
			out.Fail(out.InvalidValue, "invalid_value", perr.Error())
		}
		engine.Ask = h.Asker(p, hub.ChatOptions{HistoryLimit: 100})
	}

	ctx := context.Background()
	if resumeID == "" {
		run = flow.NewRun(f, inputs)
		engine.Start(ctx, run)
	} else {
		engine.Resume(ctx, run, inputs)
	}

	saved, serr := h.SaveRun(run)
	if serr != nil {
		fail(serr, "save run")
	}
	report(saved)
}

// report emits the run, and turns a paused or failed run into the exit code
// that says what to do about it: 0 finished, 92 needs input, 105 failed.
func report(r *flow.Run) {
	fields := map[string]any{
		"run": r.ID, "flow": r.Flow, "status": r.Status,
		"steps": r.Steps, "llm_calls": r.LLMCalls, "tokens": r.Tokens,
		"data": r.Data, "messages": r.Messages, "trace": r.Trace,
	}
	switch r.Status {
	case flow.StatusPaused:
		fields["needs"] = r.Needs
		b, _ := json.Marshal(fields)
		// Needing input is not a failure, but it is not a finished run
		// either, and an agent must be able to tell the two apart from the
		// exit code alone.
		out.Fail(out.NotFound, "needs_input",
			"run "+r.ID+" needs "+r.Needs.Var+": "+r.Needs.Prompt,
			"ahub flow resume "+r.ID+" --input "+r.Needs.Var+"=<value>",
			string(b))
	case flow.StatusFailed:
		fields["error"] = r.Error
		b, _ := json.Marshal(fields)
		out.Fail(out.ExternalError, "flow_failed", r.Error,
			"ahub flow runs --status failed", string(b))
	}
	out.Data(fields)
}

func usesAsk(f *flow.Flow) bool {
	for _, s := range f.Steps {
		if s.Type == flow.TypeAsk {
			return true
		}
	}
	return false
}

// inputPairs reads repeated --input k=v pairs.
func inputPairs(fl *flags) map[string]string {
	in := map[string]string{}
	for _, raw := range fl.list("input") {
		k, v, ok := strings.Cut(raw, "=")
		if !ok {
			out.Fail(out.InvalidValue, "invalid_value",
				"--input takes key=value, got "+raw)
		}
		in[k] = v
	}
	return in
}

func flowRef(fl *flags) string {
	if v := fl.str("flow"); v != "" {
		return v
	}
	return fl.arg(1)
}

// readAll is io.ReadAll under another name, kept here so cmd_flow.go does not
// pull io into a file that otherwise only speaks in flows.
func readAll(f *os.File) ([]byte, error) { return io.ReadAll(f) }

// errNoInput is what a definition-reading command says when it would
// otherwise block on a terminal.
var errNoInput = errors.New("no definition given: pass --file <path>, or pipe JSON on stdin")
