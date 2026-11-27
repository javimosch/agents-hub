package main

import (
	"slices"
	"strings"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/eval"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

const fromThreadExample = "ahub eval create --from-thread <id> --name <eval> --expect 'contains=...'"

// evalFromThread turns what a thread was asked into a case, so a bad run
// becomes a test. It holds the case to the rule a test is held to: it must
// fail on the run that prompted it, or it would not have caught it.
func evalFromThread(h *hub.Hub, fl *flags) {
	id, name := fl.str("from-thread"), fl.str("name")
	if id == "" || name == "" || len(fl.list("expect")) == 0 {
		// A case that asserts nothing can never fail, and what should have
		// happened is not something the thread can say.
		out.Fail(out.ValidationError, "validation_error",
			"--from-thread needs --name and at least one --expect", fromThreadExample)
	}
	var expects []eval.Expect
	for _, text := range fl.list("expect") {
		e, err := eval.ParseExpect(text)
		if err != nil {
			out.Fail(out.InvalidValue, "invalid_value", err.Error(),
				"operators: "+strings.Join(eval.Ops(), ", "))
		}
		expects = append(expects, e)
	}

	th, err := h.GetThread(id)
	if err != nil {
		fail(err, "load thread")
	}
	msgs, err := h.History(id, 0)
	if err != nil {
		fail(err, "load history")
	}
	record, err := hub.ReadThread(msgs)
	if err != nil {
		out.Fail(out.ValidationError, "invalid_thread", err.Error())
	}

	// Tried on the original wherever that costs nothing. A judge assertion
	// needs a model, so it is saved untried.
	fails, untried := false, false
	for _, e := range expects {
		if eval.Costly(e.Op) {
			untried = true
		} else if !eval.Check(e, record.Outcome, nil).Pass {
			fails = true
		}
	}
	if !fails && !untried && !fl.has("allow-passing") {
		out.Fail(out.ValidationError, "expectations_pass",
			"every expectation passes on this thread, so the case would not catch what happened in it",
			"tighten --expect, or add --allow-passing to pin behaviour that was right")
	}

	s, err := h.FindEval(name)
	switch {
	case bkn.NotFound(err):
		agent := fl.str("agent")
		if agent == "" {
			agent = th.Agent
		}
		s = &eval.Scenario{Name: name, Agent: agent}
	case err != nil:
		fail(err, "load eval")
	}
	c := eval.Case{Name: fl.str("case"), Message: record.Question, Expect: expects}
	if c.Name == "" {
		c.Name = "thread-" + id
	}
	if i := slices.IndexFunc(s.Cases, func(x eval.Case) bool { return x.Name == c.Name }); i >= 0 {
		s.Cases[i] = c
	} else {
		s.Cases = append(s.Cases, c)
	}
	// The thread may have had tools its agent does not carry, as a scheduled
	// run does. Without them the case would measure something else.
	for _, tool := range record.Outcome.Tools {
		if !slices.Contains(s.Tools, tool) {
			s.Tools = append(s.Tools, tool)
		}
	}
	saved, err := h.SaveEval(s)
	if err != nil {
		fail(err, "save eval")
	}
	if record.Questions > 1 {
		out.Log("the thread asked %d questions; the case replays only the first", record.Questions)
	}
	original := "fails"
	if !fails {
		original = "passes"
		if untried {
			original = "untried"
		}
	}
	out.Data(map[string]any{"eval": map[string]any{
		"id": saved.ID, "name": saved.Name, "agent": saved.Agent,
		"cases": len(saved.Cases), "tools": saved.Tools},
		"case": c.Name, "on_the_original": original})
}
