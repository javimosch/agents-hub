package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/javimosch/agents-hub/internal/eval"
)

var fixedNow = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

const budget = 1_000_000

func wantSpend(t *testing.T, h *Hub, tokens, turns int) {
	t.Helper()
	s, err := h.SpentToday()
	if err != nil {
		t.Fatal(err)
	}
	if s.Tokens != tokens || s.Turns != turns {
		t.Fatalf("spent %d tokens in %d turns, want %d in %d", s.Tokens, s.Turns, tokens, turns)
	}
}

func TestEveryWayOutOfATurnRecordsItsSpend(t *testing.T) {
	down := step{err: errors.New("provider on fire")}
	cases := []struct {
		name      string
		steps     []step
		tools     bool
		maxSteps  int
		wantErr   bool
		wantSpend int
	}{
		{"answered", []step{answer("a", 10)}, false, 0, false, 10},
		{"tool loop", []step{asksFor(10, "c1"), answer("a", 5)}, true, 0, false, 15},
		{"max steps", []step{asksFor(10, "c1")}, true, 1, false, 10},
		{"fails after one step", []step{asksFor(10, "c1"), down}, true, 0, true, 10},
		{"tool never offered", []step{asksFor(10, "c1")}, false, 0, true, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHub(t)
			opts := ChatOptions{Agent: Agent{Model: "m"}, Message: "go",
				DailyBudget: budget, MaxSteps: tc.maxSteps}
			if tc.tools {
				opts.Tools = noopRegistry(new(int))
			}
			_, err := h.Chat(context.Background(), &scripted{steps: tc.steps}, opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			wantSpend(t, h, tc.wantSpend, 1)
		})
	}
}

// full records that the thread's last prompt filled the window. It is stored,
// because a turn reads the thread again before it uses it.
func full(t *testing.T, h *Hub, th *Thread) {
	t.Helper()
	th.LastPrompt = DefaultContextLimit
	if _, err := h.C.Patch(RefThreads, th.ID, map[string]any{"last_prompt_tokens": th.LastPrompt}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionTokensCountAgainstTheBudget(t *testing.T) {
	h, f := newTestHub(t)
	th := seedThread(f, 10)
	full(t, h, &th) // over the threshold, so it folds first
	p := &scripted{steps: []step{answer("summary", 100), answer("a", 7)}}
	if _, err := h.Chat(context.Background(), p, ChatOptions{
		Agent: Agent{Model: "m"}, Thread: &th, Message: "go",
		Compact: &CompactPolicy{}, DailyBudget: budget}); err != nil {
		t.Fatal(err)
	}
	wantSpend(t, h, 107, 1)
}

func TestUnusableSummaryStillCostsTokens(t *testing.T) {
	h, f := newTestHub(t)
	th := seedThread(f, 10)
	full(t, h, &th)
	p := &scripted{steps: []step{answer("", 100), answer("a", 7)}}
	if _, err := h.Chat(context.Background(), p, ChatOptions{
		Agent: Agent{Model: "m"}, Thread: &th, Message: "go",
		Compact: &CompactPolicy{}, DailyBudget: budget}); err != nil {
		t.Fatal(err)
	}
	wantSpend(t, h, 107, 1)
}

func TestEvalJudgeCountsAgainstTheBudget(t *testing.T) {
	h, _ := newTestHub(t)
	p := &scripted{steps: []step{answer("PASS\nfine", 30)}}
	judge := h.judgeFor(context.Background(), EvalOptions{
		Provider: p, Judge: &Agent{Model: "j"}, DailyBudget: budget,
		Scenario: &eval.Scenario{}}, &EvalRun{})
	if _, _, _, err := judge("q", "a"); err != nil {
		t.Fatal(err)
	}
	wantSpend(t, h, 30, 1)
}

func TestSpentBudgetRefusesBeforeCallingTheProvider(t *testing.T) {
	h, _ := newTestHub(t)
	if err := h.RecordSpend(10); err != nil {
		t.Fatal(err)
	}
	p := &scripted{steps: []step{answer("a", 1)}}
	_, err := h.Chat(context.Background(), p, ChatOptions{
		Agent: Agent{Model: "m"}, Message: "go", DailyBudget: 10})
	var over *BudgetError
	if !errors.As(err, &over) || len(p.seen) != 0 {
		t.Fatalf("err = %v, provider calls = %d", err, len(p.seen))
	}
}
