package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/javimosch/agents-hub/internal/provider"
)

func storedCalls(ids ...string) string {
	calls := []provider.ToolCall{}
	for _, id := range ids {
		calls = append(calls, provider.ToolCall{ID: id, Name: "noop", Arguments: "{}"})
	}
	b, _ := json.Marshal(calls)
	return string(b)
}

// threadWith stores a thread holding exactly these messages, in order.
func threadWith(t *testing.T, h *Hub, f *fakeBKN, msgs ...Message) Thread {
	t.Helper()
	id := f.seed(RefThreads, map[string]any{"agent": "a"})
	for i, m := range msgs {
		m.ThreadID, m.Seq = id, i
		if _, err := h.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	th, err := h.GetThread(id)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func TestAThreadCutOffMidToolExchangeCanBeResumed(t *testing.T) {
	user := Message{Role: provider.RoleUser, Content: "go"}
	asked := Message{Role: provider.RoleAssistant, ToolCalls: storedCalls("c1", "c2")}
	result := Message{Role: provider.RoleTool, ToolCallID: "c1", Content: "done"}
	stray := Message{Role: provider.RoleTool, ToolCallID: "zz", Content: "whose?"}
	cases := map[string][]Message{
		"no result stored":             {user, asked},
		"one of two results stored":    {user, asked, result},
		"cut off, then another turn":   {user, asked, user, {Role: provider.RoleAssistant, Content: "ok"}},
		"a result nobody asked for":    {user, stray},
		"the same result stored twice": {user, asked, result, result},
	}
	for name, msgs := range cases {
		t.Run(name, func(t *testing.T) {
			h, f := newTestHub(t)
			th := threadWith(t, h, f, msgs...)
			// scripted rejects unpaired calls, as a real provider does.
			p := &scripted{steps: []step{answer("resumed", 1)}}
			turn, err := h.Chat(context.Background(), p, ChatOptions{
				Agent: Agent{Model: "m"}, Thread: &th, Message: "again"})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if turn.Reply.Content != "resumed" {
				t.Fatalf("reply = %q", turn.Reply.Content)
			}
		})
	}
}

func TestCompactionKeepsAToolCallWithItsResults(t *testing.T) {
	// keep=2 would fold the call and keep only its second result.
	for keep := 1; keep <= 4; keep++ {
		t.Run(fmt.Sprint("keep ", keep), func(t *testing.T) {
			h, f := newTestHub(t)
			th := threadWith(t, h, f,
				Message{Role: provider.RoleUser, Content: "one"},
				Message{Role: provider.RoleAssistant, Content: "two"},
				Message{Role: provider.RoleAssistant, ToolCalls: storedCalls("c1", "c2")},
				Message{Role: provider.RoleTool, ToolCallID: "c1", Content: "r1"},
				Message{Role: provider.RoleTool, ToolCallID: "c2", Content: "r2"},
				Message{Role: provider.RoleAssistant, Content: "three"})
			p := &scripted{steps: []step{answer("notes", 1)}}
			if _, err := h.Compact(context.Background(), p, &th, Agent{Model: "m"},
				CompactPolicy{Keep: keep}, "test"); err != nil {
				t.Fatal(err)
			}
			kept, err := h.HistoryAfter(th.ID, th.SummarySeq, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(kept) > 0 && kept[0].Role == provider.RoleTool {
				t.Errorf("kept history opens on the result of a folded call (folded up to %d)", th.SummarySeq)
			}
		})
	}
}

// during runs a function while the model is "thinking".
type during struct {
	provider.Provider
	do func()
}

func (d during) Chat(ctx context.Context, r provider.Request, on func(string)) (*provider.Response, error) {
	d.do()
	return d.Provider.Chat(ctx, r, on)
}

func TestASecondTurnOnABusyThreadIsRefused(t *testing.T) {
	h, f := newTestHub(t)
	th := threadWith(t, h, f)
	var second error
	first := during{&scripted{steps: []step{answer("one", 1)}}, func() {
		other := th
		_, second = h.Chat(context.Background(), &scripted{steps: []step{answer("two", 1)}},
			ChatOptions{Agent: Agent{Model: "m"}, Thread: &other, Message: "me too"})
	}}
	mine := th
	if _, err := h.Chat(context.Background(), first, ChatOptions{
		Agent: Agent{Model: "m"}, Thread: &mine, Message: "first"}); err != nil {
		t.Fatal(err)
	}
	var busy *BusyError
	if !errors.As(second, &busy) {
		t.Fatalf("second turn: %v, want a BusyError", second)
	}
	seqs := map[int]bool{}
	for _, m := range f.all(RefMessages) {
		seq := int(m.Num("seq"))
		if seqs[seq] {
			t.Errorf("seq %d is used twice", seq)
		}
		seqs[seq] = true
	}
}

// A caller may have read the thread before another turn finished with it.
func TestATurnUsesTheThreadAsItIsNowNotAsTheCallerReadIt(t *testing.T) {
	h, f := newTestHub(t)
	stale := threadWith(t, h, f, Message{Role: provider.RoleUser, Content: "old"},
		Message{Role: provider.RoleAssistant, Content: "reply"})
	if _, err := h.C.Patch(RefThreads, stale.ID, map[string]any{"summary": "fresh notes", "summary_seq": 1}); err != nil {
		t.Fatal(err)
	}
	p := &scripted{steps: []step{answer("ok", 1)}}
	if _, err := h.Chat(context.Background(), p, ChatOptions{
		Agent: Agent{Model: "m"}, Thread: &stale, Message: "next"}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.seen[0]); n != 2 { // the notes and the new question
		t.Errorf("prompt has %d messages, want 2: %+v", n, p.seen[0])
	}
}

func TestTheThreadIsFreeAfterATurnHoweverItEnds(t *testing.T) {
	for name, s := range map[string]step{"answered": answer("a", 1), "failed": {err: fmt.Errorf("down")}} {
		t.Run(name, func(t *testing.T) {
			h, f := newTestHub(t)
			th := threadWith(t, h, f)
			_, _ = h.Chat(context.Background(), &scripted{steps: []step{s}}, ChatOptions{
				Agent: Agent{Model: "m"}, Thread: &th, Message: "go"})
			if got := f.all(RefThreads)[0].Str("lease"); got != "" {
				t.Errorf("lease left held: %q", got)
			}
		})
	}
}

// deadPID is the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestWhoMayTakeOverALease(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	cases := map[string]struct {
		held string
		want bool
	}{
		"a dead process here": {leaseValue(fmt.Sprintf("%s:%d#%s.x", host, deadPID(t), processScope()), later), true},
		// Same hostname and a pid unused here, but another machine or container.
		"same name, other scope":  {leaseValue(fmt.Sprintf("%s:%d#elsewhere.x", host, deadPID(t)), later), false},
		"a live process here":     {leaseValue(fmt.Sprintf("%s:%d#%s.x", host, os.Getpid(), processScope()), later), false},
		"another host, unexpired": {leaseValue("elsewhere:1#x", later), false},
		"another host, expired":   {leaseValue("elsewhere:1#x", time.Now().Add(-time.Hour)), true},
		"an owner with no pid":    {leaseValue(host, later), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, f := newTestHub(t)
			id := f.seed(RefSchedules, map[string]any{leaseField: c.held})
			got, err := h.AcquireLease(id, Owner(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if got.Held != c.want {
				t.Errorf("held = %v, want %v", got.Held, c.want)
			}
		})
	}
}

func TestTwoClaimsFromOneProcessDoNotBothWin(t *testing.T) {
	h, f := newTestHub(t)
	id := f.seed(RefSchedules, map[string]any{})
	a, err := h.AcquireLease(id, Owner(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.AcquireLease(id, Owner(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Held || b.Held {
		t.Errorf("held = %v then %v, want true then false", a.Held, b.Held)
	}
}

// racing lets a rival claim an abandoned lease just before our takeover lands.
func TestATakeoverThatLosesTheRaceDoesNotClaimTheLease(t *testing.T) {
	h, f := newTestHub(t)
	id := f.seed(RefSchedules, map[string]any{
		leaseField: leaseValue("elsewhere:1#x", time.Now().Add(-time.Hour))})
	rival := leaseValue("rival:2#y", time.Now().Add(time.Hour))
	f.beforeGuarded = func(doc map[string]any) { doc[leaseField] = rival }
	got, err := h.AcquireLease(id, Owner(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Held {
		t.Error("claimed a lease the rival had just taken")
	}
}

func TestReleasingDoesNotClearSomebodyElsesLease(t *testing.T) {
	h, f := newTestHub(t)
	id := f.seed(RefSchedules, map[string]any{})
	mine, err := h.AcquireLease(id, Owner(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	theirs := leaseValue("other:2#y", time.Now().Add(time.Hour))
	f.all(RefSchedules)[0][leaseField] = theirs
	if err := h.ReleaseLease(id, mine); err != nil {
		t.Fatal(err)
	}
	if got := f.all(RefSchedules)[0].Str(leaseField); got != theirs {
		t.Errorf("lease = %q, want %q", got, theirs)
	}
}

func TestAFailedScheduledRunNamesItsThread(t *testing.T) {
	h, f := newTestHub(t)
	f.seed(RefAgents, map[string]any{"name": "a", "model": "m"})
	id := f.seed(RefSchedules, map[string]any{"name": "s"})
	run, err := h.RunSchedule(context.Background(), RunScheduleOptions{
		Schedule: Schedule{ID: id, Name: "s", Agent: "a", EverySecs: 60, Prompt: "go"},
		Provider: &scripted{steps: []step{{err: fmt.Errorf("provider down")}}}, Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if want := f.all(RefThreads)[0].Str("id"); run.Thread != want {
		t.Errorf("run.Thread = %q, want %q", run.Thread, want)
	}
}
