package hub

import (
	"context"
	"errors"
	"testing"
)

// A run that cannot be set up is still a run: it leaves a record and the
// schedule moves on, and the model is never paid for.
func TestARunThatCannotBeSetUpIsRecordedAsFailed(t *testing.T) {
	h, f := newTestHub(t)
	id := f.seed(RefSchedules, map[string]any{"name": "s"})
	p := &scripted{steps: []step{answer("should not run", 1)}}

	run, err := h.RunSchedule(context.Background(), RunScheduleOptions{
		Schedule: Schedule{ID: id, Name: "s", Agent: "a", EverySecs: 60},
		Provider: p, Trigger: "schedule", SetupError: errors.New("no memgraph here"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.Error != "no memgraph here" {
		t.Errorf("run = %q %q, want failed with the setup error", run.Status, run.Error)
	}
	if n := len(f.all(RefScheduleRuns)); n != 1 {
		t.Errorf("%d runs recorded, want 1", n)
	}
	if next := f.all(RefSchedules)[0].Str("next_run_at"); next == "" {
		t.Error("the schedule was not advanced")
	}
	if len(p.seen) != 0 {
		t.Errorf("the model was called %d times, want 0", len(p.seen))
	}
}
