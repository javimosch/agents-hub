package hub

import (
	"context"
	"fmt"
	"testing"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/provider"
)

// seedThread stores a thread of n alternating messages with seq 0..n-1.
func seedThread(f *fakeBKN, n int) Thread {
	id := f.seed(RefThreads, bkn.Record{"messages": float64(n)})
	for i := 0; i < n; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		f.seed(RefMessages, bkn.Record{"thread_id": id, "role": role,
			"content": fmt.Sprintf("m%d", i), "seq": float64(i)})
	}
	return Thread{ID: id, Messages: n}
}

func TestHistoryReturnsEveryMessage(t *testing.T) {
	for _, n := range []int{0, 49, 50, 51, historyPage, historyPage + 1, 2*historyPage + 7} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h, f := newTestHub(t)
			th := seedThread(f, n)
			got, err := h.History(th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != n {
				t.Fatalf("got %d messages, want %d", len(got), n)
			}
			for i, m := range got {
				if m.Seq != i {
					t.Fatalf("position %d holds seq %d", i, m.Seq)
				}
			}
		})
	}
}

func TestChatOnLongThreadSeesRecentTurnsAndNumbersOn(t *testing.T) {
	h, f := newTestHub(t)
	th := seedThread(f, 60)
	p := &scripted{steps: []step{answer("ok", 1)}}

	turn, err := h.Chat(context.Background(), p, ChatOptions{
		Agent: Agent{Model: "m"}, Thread: &th, Message: "new", HistoryLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	sent := p.seen[0]
	if got := sent[len(sent)-2].Content; got != "m59" {
		t.Errorf("last replayed message = %q, want m59", got)
	}
	if turn.User.Seq != 60 || turn.Reply.Seq != 61 {
		t.Errorf("seqs = %d,%d want 60,61", turn.User.Seq, turn.Reply.Seq)
	}
}

func TestDeleteThreadRemovesEveryMessage(t *testing.T) {
	h, f := newTestHub(t)
	th := seedThread(f, 120)
	deleted, err := h.DeleteThread(th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if left := len(f.all(RefMessages)); deleted != 120 || left != 0 {
		t.Fatalf("deleted %d, left %d; want 120, 0", deleted, left)
	}
}

func TestDueSchedulesLooksPastTheFirstPage(t *testing.T) {
	h, f := newTestHub(t)
	for i := 0; i < 60; i++ {
		f.seed(RefSchedules, bkn.Record{"name": fmt.Sprintf("s%02d", i),
			"enabled": true, "next_run_at": "2000-01-01T00:00:00Z"})
	}
	due, err := h.DueSchedules(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 60 {
		t.Fatalf("got %d due schedules, want 60", len(due))
	}
}

// Paging makes --history bite on long threads for the first time, so the
// window must not open on a tool result whose call was cut off.
func TestHistoryWindowNeverStartsWithAnOrphanToolResult(t *testing.T) {
	h, f := newTestHub(t)
	th := seedThread(f, 2)
	f.seed(RefMessages, bkn.Record{"thread_id": th.ID, "role": provider.RoleAssistant, "seq": 2.0,
		"tool_calls": `[{"id":"c1","name":"noop","arguments":"{}"},{"id":"c2","name":"noop","arguments":"{}"}]`})
	for i, id := range []string{"c1", "c2"} {
		f.seed(RefMessages, bkn.Record{"thread_id": th.ID, "role": provider.RoleTool,
			"tool_call_id": id, "seq": float64(3 + i)})
	}
	f.seed(RefMessages, bkn.Record{"thread_id": th.ID, "role": provider.RoleAssistant, "content": "done", "seq": 5.0})

	for limit := 1; limit <= 4; limit++ {
		p := &scripted{steps: []step{answer("ok", 1)}}
		cp := th
		turn, err := h.Chat(context.Background(), p, ChatOptions{
			Agent: Agent{Model: "m"}, Thread: &cp, Message: "next", HistoryLimit: limit})
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if turn.User.Seq <= 5 {
			t.Fatalf("limit %d: reused seq %d", limit, turn.User.Seq)
		}
		// Drop what this turn stored so each limit sees the same thread.
		for _, d := range f.all(RefMessages) {
			if d.Num("seq") > 5 {
				if err := h.C.Delete(RefMessages, d.Str("id")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
