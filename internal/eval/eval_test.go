package eval_test

import (
	"strings"
	"testing"

	"github.com/javimosch/agents-hub/internal/eval"
)

func out(answer string) eval.Outcome {
	return eval.Outcome{Answer: answer, Tokens: 100, Steps: 2, Tools: []string{"memory_recall"}}
}

func TestDeterministicAssertions(t *testing.T) {
	o := out("Wrap the loop body in an arena { } block.")
	cases := []struct {
		e    eval.Expect
		want bool
	}{
		{eval.Expect{Op: eval.OpContains, Value: "arena"}, true},
		{eval.Expect{Op: eval.OpContains, Value: "ARENA"}, true}, // case-insensitive on purpose
		{eval.Expect{Op: eval.OpContains, Value: "goroutine"}, false},
		{eval.Expect{Op: eval.OpNotContains, Value: "garbage collector"}, true},
		{eval.Expect{Op: eval.OpNotContains, Value: "arena"}, false},
		{eval.Expect{Op: eval.OpMatches, Value: `arena\s*\{`}, true},
		{eval.Expect{Op: eval.OpMatches, Value: `^never`}, false},
		{eval.Expect{Op: eval.OpMaxTokens, Value: "100"}, true},
		{eval.Expect{Op: eval.OpMaxTokens, Value: "99"}, false},
		{eval.Expect{Op: eval.OpMaxSteps, Value: "2"}, true},
		{eval.Expect{Op: eval.OpMaxSteps, Value: "1"}, false},
		{eval.Expect{Op: eval.OpToolUsed, Value: "memory_recall"}, true},
		{eval.Expect{Op: eval.OpToolUsed, Value: "skill_show"}, false},
		{eval.Expect{Op: eval.OpToolNotUsed, Value: "memory_save"}, true},
		{eval.Expect{Op: eval.OpToolNotUsed, Value: "memory_recall"}, false},
	}
	for _, c := range cases {
		got := eval.Check(c.e, o, nil)
		if got.Pass != c.want {
			t.Errorf("%s %q: pass=%v want %v (%s)", c.e.Op, c.e.Value, got.Pass, c.want, got.Detail)
		}
		if eval.Costly(c.e.Op) {
			t.Errorf("%s should not be costly", c.e.Op)
		}
		if got.Tokens != 0 {
			t.Errorf("%s spent %d tokens; deterministic assertions must be free", c.e.Op, got.Tokens)
		}
	}
}

// A failing assertion has to say enough to act on. "false" is not a report.
func TestFailuresExplainThemselves(t *testing.T) {
	o := out("a short answer")
	for _, e := range []eval.Expect{
		{Op: eval.OpContains, Value: "arena"},
		{Op: eval.OpMaxTokens, Value: "10"},
		{Op: eval.OpToolUsed, Value: "skill_show"},
	} {
		r := eval.Check(e, o, nil)
		if r.Pass || r.Detail == "" {
			t.Errorf("%s: expected a failure with a detail, got pass=%v detail=%q", e.Op, r.Pass, r.Detail)
		}
	}
}

func TestJudgeIsTheOnlyCostlyOp(t *testing.T) {
	called := ""
	judge := func(q, a string) (bool, string, int, error) {
		called = q
		return true, "the answer covers it", 42, nil
	}
	r := eval.Check(eval.Expect{Op: eval.OpJudge, Value: "Does it mention arenas?"}, out("arena"), judge)
	if !r.Pass || r.Tokens != 42 || called == "" {
		t.Fatalf("judge not wired: %+v (called %q)", r, called)
	}
	if !eval.Costly(eval.OpJudge) {
		t.Fatal("judge must be reported as costly")
	}
	// With no judge available the assertion fails rather than passing by
	// default: a check that silently becomes a no-op is worse than no check.
	if r := eval.Check(eval.Expect{Op: eval.OpJudge, Value: "x"}, out("y"), nil); r.Pass {
		t.Fatal("a judge assertion with no judge must not pass")
	}
}

func TestValidateCatchesWhatWouldOnlyFailAfterPaying(t *testing.T) {
	s, err := eval.Parse([]byte(`{
      "name":"s","agent":"helper",
      "cases":[
        {"name":"a","message":"m","expect":[{"op":"matches","value":"[unclosed"}]},
        {"name":"a","message":"m","expect":[{"op":"max_tokens","value":"lots"}]},
        {"name":"b","message":"m","expect":[{"op":"nonsense","value":"x"}]},
        {"name":"c","message":"m","expect":[{"op":"judge","value":"is it good?"}]}
      ]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var msgs []string
	for _, p := range eval.Validate(s) {
		msgs = append(msgs, p.Message)
	}
	joined := strings.Join(msgs, " | ")
	for _, want := range []string{"error parsing regexp", "needs a number", "unknown op", "duplicate case", "names no judge"} {
		if !strings.Contains(joined, want) {
			t.Errorf("validator missed %q; got: %s", want, joined)
		}
	}
	if !eval.Fatal(eval.Validate(s)) {
		t.Fatal("a bad regex and an unknown op should be fatal")
	}
}

// A scenario that asserts nothing is a warning, not an error: it still runs,
// it just cannot fail, and someone should know that.
func TestEmptyExpectIsAWarning(t *testing.T) {
	s, _ := eval.Parse([]byte(`{"name":"s","agent":"a","cases":[{"name":"c","message":"m","expect":[]}]}`))
	ps := eval.Validate(s)
	if eval.Fatal(ps) {
		t.Fatalf("should not be fatal: %+v", ps)
	}
	found := false
	for _, p := range ps {
		if strings.Contains(p.Message, "can never fail") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning about an assertionless case: %+v", ps)
	}
}
