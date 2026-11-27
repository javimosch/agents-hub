package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// leaseField holds "<owner>#<scope>.<nonce>|<expiry>" in a single field,
// because a lease has to be taken or refused in ONE atomic operation. Two
// fields would mean two writes and a window between them.
const leaseField = "lease"

// LeaseTTL is how long a lease is honoured without being renewed. It matters
// only when the holder cannot be seen to be dead: a process that died on
// another machine.
const LeaseTTL = 30 * time.Minute

// Lease is a claim on one record: a schedule, or a thread.
type Lease struct {
	Owner   string
	Expires time.Time
	Held    bool
	HeldBy  string

	ref, id, value string
	ttl            time.Duration
}

// BusyError says somebody else is working on the record.
type BusyError struct{ Holder string }

func (e *BusyError) Error() string { return "already in use by " + e.Holder }

func leaseValue(owner string, until time.Time) string {
	return owner + "|" + until.UTC().Format(time.RFC3339)
}

func parseLease(raw string) (claim string, expires time.Time) {
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

// processScope identifies the set of processes a pid is meaningful in: this
// boot and this pid namespace. Hostnames are not enough, since containers and
// cloned machines share them. Empty when it cannot be told.
var processScope = sync.OnceValue(func() string {
	boot, err1 := os.ReadFile("/proc/sys/kernel/random/boot_id")
	ns, err2 := os.Readlink("/proc/self/ns/pid")
	if err1 != nil || err2 != nil {
		return ""
	}
	sum := sha256.Sum256(append(boot, ns...))
	return hex.EncodeToString(sum[:6])
})

// holderIsGone is true only when the holder certainly no longer exists.
func holderIsGone(claim string) bool {
	owner, tag, _ := strings.Cut(claim, "#")
	scope, _, _ := strings.Cut(tag, ".")
	if scope == "" || scope != processScope() {
		return false
	}
	pid, err := strconv.Atoi(owner[strings.LastIndex(owner, ":")+1:])
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == syscall.ESRCH
}

func (h *Hub) acquire(ref, id, owner string, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		ttl = LeaseTTL
	}
	// The nonce tells two claims by one process apart.
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Lease{}, err
	}
	claim := owner + "#" + processScope() + "." + hex.EncodeToString(nonce[:])
	want := leaseValue(claim, time.Now().Add(ttl))
	mine := Lease{Owner: owner, Held: true, ref: ref, id: id, value: want, ttl: ttl}

	// $setIfEmpty writes only into an empty field and returns what the field
	// then holds, so there is no window between looking and taking.
	rec, err := h.C.Patch(ref, id, map[string]any{
		leaseField: map[string]any{"$setIfEmpty": want},
	})
	if err != nil {
		return Lease{}, err
	}
	got := rec.Str(leaseField)
	if got == want {
		return mine, nil
	}
	holder, expires := parseLease(got)
	name, _, _ := strings.Cut(holder, "#")
	refused := Lease{HeldBy: name, Expires: expires}
	abandoned := holderIsGone(holder) || (!expires.IsZero() && time.Now().After(expires))
	if !abandoned {
		return refused, nil
	}
	// Taken over only if it is still the lease just judged abandoned.
	if _, err := h.C.PatchIf(ref, id, map[string]any{leaseField: want}, leaseField, got); err != nil {
		if bkn.GuardFailed(err) {
			return refused, nil
		}
		return Lease{}, err
	}
	return mine, nil
}

// AcquireLease claims a schedule, or reports who already has it.
func (h *Hub) AcquireLease(scheduleID, owner string, ttl time.Duration) (Lease, error) {
	return h.acquire(RefSchedules, scheduleID, owner, ttl)
}

// RenewLease pushes the expiry out. It fails when the lease is no longer ours,
// and the holder must then stop writing.
func (h *Hub) RenewLease(l *Lease) error {
	claim, _ := parseLease(l.value)
	next := leaseValue(claim, time.Now().Add(l.ttl))
	if _, err := h.C.PatchIf(l.ref, l.id, map[string]any{leaseField: next}, leaseField, l.value); err != nil {
		if bkn.GuardFailed(err) {
			return fmt.Errorf("the lease on %s %s was lost to another process", l.ref, l.id)
		}
		return err
	}
	l.value = next
	return nil
}

// release gives a lease back, and leaves alone one that is no longer ours.
func (h *Hub) release(l Lease) error {
	_, err := h.C.PatchIf(l.ref, l.id, map[string]any{leaseField: ""}, leaseField, l.value)
	if bkn.GuardFailed(err) {
		return nil
	}
	return err
}

// ReleaseLease gives a schedule back.
func (h *Hub) ReleaseLease(_ string, l Lease) error { return h.release(l) }
