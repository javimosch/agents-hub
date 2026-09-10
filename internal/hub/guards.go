package hub

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// RefSpend is the daily token counter.
//
// One document per day, incremented atomically, so two processes spending at
// once cannot lose an increment between a read and a write -- which is
// precisely what a budget must not do.
const RefSpend = NS + "/spend"

// leaseField holds "<owner>|<expiry>" in a single field, because the lease has
// to be taken or refused in ONE atomic operation. Two fields would mean two
// writes and a window between them.
const leaseField = "lease"

// LeaseTTL is how long a lease is honoured before it is assumed abandoned. A
// process killed mid-run cannot release its own lease, so without an expiry a
// single crash would stop a schedule forever.
const LeaseTTL = 30 * time.Minute

// Lease is a claim on one schedule.
type Lease struct {
	Owner   string
	Expires time.Time
	Held    bool
	HeldBy  string
}

func leaseValue(owner string, until time.Time) string {
	return owner + "|" + until.UTC().Format(time.RFC3339)
}

func parseLease(raw string) (owner string, expires time.Time) {
	o, t, ok := strings.Cut(raw, "|")
	if !ok {
		return raw, time.Time{}
	}
	exp, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return o, time.Time{}
	}
	return o, exp
}

// Owner names this process for a lease: the host and the pid, so a held lease
// says who is holding it rather than only that somebody is.
func Owner() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// AcquireLease claims a schedule, or reports who already has it.
//
// $setIfEmpty is the whole mechanism: the field is written only if it is
// missing, null or empty, and the patch returns whatever the field ended up
// holding. Comparing that to what we asked for tells us whether we won --
// atomically, with no read-then-write window for a second ticker to slip
// through. A lease that has expired is taken over, because the process that
// held it is gone and will never come back to release it.
func (h *Hub) AcquireLease(scheduleID, owner string, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		ttl = LeaseTTL
	}
	want := leaseValue(owner, time.Now().Add(ttl))
	rec, err := h.C.Patch(RefSchedules, scheduleID, map[string]any{
		leaseField: map[string]any{"$setIfEmpty": want},
	})
	if err != nil {
		return Lease{}, err
	}
	got := rec.Str(leaseField)
	if got == want {
		return Lease{Owner: owner, Held: true}, nil
	}
	holder, expires := parseLease(got)
	if !expires.IsZero() && time.Now().After(expires) {
		// Abandoned. Overwrite it rather than waiting for a process that is
		// not coming back.
		if _, err := h.C.Patch(RefSchedules, scheduleID, map[string]any{leaseField: want}); err != nil {
			return Lease{}, err
		}
		return Lease{Owner: owner, Held: true}, nil
	}
	return Lease{Held: false, HeldBy: holder, Expires: expires}, nil
}

// ReleaseLease gives a schedule back. It clears the field unconditionally:
// only the holder calls this, and a lease left behind by a crash is handled by
// its expiry instead.
func (h *Hub) ReleaseLease(scheduleID string) error {
	_, err := h.C.Patch(RefSchedules, scheduleID, map[string]any{leaseField: ""})
	return err
}

// --- spending -------------------------------------------------------------

// Spend is what a day has cost.
type Spend struct {
	Day    string `json:"day"`
	Tokens int    `json:"tokens"`
	Turns  int    `json:"turns"`
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// SpentToday reads the running total. A missing document is zero, not an
// error: the first turn of the day has not written one yet.
func (h *Hub) SpentToday() (Spend, error) {
	q := url.Values{}
	q.Set("day", today())
	recs, err := h.C.List(RefSpend, q)
	if err != nil {
		return Spend{}, err
	}
	if len(recs) == 0 {
		return Spend{Day: today()}, nil
	}
	return Spend{Day: today(), Tokens: int(recs[0].Num("tokens")), Turns: int(recs[0].Num("turns"))}, nil
}

// RecordSpend adds to today's total, atomically.
func (h *Hub) RecordSpend(tokens int) error {
	if tokens <= 0 {
		return nil
	}
	q := url.Values{}
	q.Set("day", today())
	recs, err := h.C.List(RefSpend, q)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		_, err := h.C.Create(RefSpend, map[string]any{
			"day": today(), "tokens": tokens, "turns": 1,
			"created_at": now(), "updated_at": now(),
		})
		// A second process may have created it in the meantime; fall through
		// to incrementing rather than failing a turn over bookkeeping.
		if err == nil {
			return nil
		}
		recs, err = h.C.List(RefSpend, q)
		if err != nil || len(recs) == 0 {
			return err
		}
	}
	_, err = h.C.Patch(RefSpend, recs[0].Str("id"), map[string]any{
		"tokens": map[string]any{"$inc": tokens},
		"turns":  map[string]any{"$inc": 1},
		// updated_at is set rather than incremented, obviously, but it matters
		// here: without it a day's document looks untouched since creation.
		"updated_at": now(),
	})
	return err
}

// BudgetError is a refusal to spend more today.
type BudgetError struct {
	Spent, Limit int
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("today's token budget is spent: %d of %d", e.Spent, e.Limit)
}

// CheckBudget refuses a turn that would exceed the day's allowance.
//
// Checked before the turn rather than after, because a budget enforced only
// after the fact is a report, not a budget. --max-steps bounds one turn; this
// is what bounds a schedule that runs every fifteen minutes while nobody is
// watching.
func (h *Hub) CheckBudget(limit int) error {
	if limit <= 0 {
		return nil
	}
	s, err := h.SpentToday()
	if err != nil {
		// A budget that cannot be read does not block work: failing every
		// turn because the counter is unreachable would be worse than the
		// overspend it is guarding against.
		return nil
	}
	if s.Tokens >= limit {
		return &BudgetError{Spent: s.Tokens, Limit: limit}
	}
	return nil
}

// --- reaping --------------------------------------------------------------

// alive reports whether a pid is still running. Signal 0 asks the kernel
// without touching the process.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// ReapJobs marks jobs whose supervisor is gone.
//
// Two cases, and both leave a record that says "running" forever otherwise:
// a job recorded but never spawned (this process died in between), and a job
// whose supervisor was killed before it could write the result.
func (h *Hub) ReapJobs(grace time.Duration) (int, error) {
	if grace <= 0 {
		grace = time.Minute
	}
	running, err := h.ListJobs(JobRunning, 100)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, j := range running {
		started, perr := time.Parse(time.RFC3339Nano, j.StartedAt)
		if perr != nil {
			started, perr = time.Parse(time.RFC3339, j.StartedAt)
		}
		young := perr == nil && time.Since(started) < grace
		switch {
		case j.PID == 0 && young:
			continue // it may be starting right now
		case j.PID == 0:
			_ = h.UpdateJob(j.ID, map[string]any{
				"status": JobFailed, "finished_at": now(),
				"error": "never started: the process that recorded it did not survive to spawn it",
			})
			reaped++
		case !alive(j.PID) && !young:
			_ = h.UpdateJob(j.ID, map[string]any{
				"status": JobFailed, "finished_at": now(),
				"error": fmt.Sprintf("supervisor %d is gone and recorded no result", j.PID),
			})
			reaped++
		}
	}
	return reaped, nil
}

// PruneJobs removes finished jobs older than a window, and their output files.
//
// Output accumulates at whatever rate a schedule runs, and a tool that budgets
// other people's disks should not quietly fill its own.
func (h *Hub) PruneJobs(olderThan time.Duration, dryRun bool) (jobs int, bytes int64, err error) {
	all, err := h.ListJobs("", 500)
	if err != nil {
		return 0, 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	for _, j := range all {
		if !j.Done() {
			continue
		}
		stamp := j.FinishedAt
		if stamp == "" {
			stamp = j.StartedAt
		}
		t, perr := time.Parse(time.RFC3339Nano, stamp)
		if perr != nil {
			t, perr = time.Parse(time.RFC3339, stamp)
		}
		if perr != nil || t.After(cutoff) {
			continue
		}
		if st, serr := os.Stat(j.Output); serr == nil {
			bytes += st.Size()
		}
		jobs++
		if dryRun {
			continue
		}
		if j.Output != "" {
			_ = os.Remove(j.Output)
			_ = os.Remove(strings.TrimSuffix(j.Output, ".out") + ".log")
		}
		if derr := h.C.Delete(RefJobs, j.ID); derr != nil {
			return jobs, bytes, derr
		}
	}
	return jobs, bytes, nil
}
