package hub

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/flow"
)

// RefJobs is where background work is recorded.
const RefJobs = NS + "/jobs"

// Job statuses.
const (
	JobRunning = "running"
	JobExited  = "exited"
	JobStopped = "stopped"
	JobFailed  = "failed"
)

// Job is a command running outside the turn that started it.
//
// It exists because a turn cannot wait. A test suite or a deploy takes minutes
// and the model has a provider timeout measured in seconds, so a synchronous
// tool call either stalls the loop or dies in it. A job outlives the process
// that started it, which also means a schedule can begin something at 03:00
// and collect it on the next tick.
type Job struct {
	ID       string   `json:"id"`
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Status   string   `json:"status"`
	PID      int      `json:"pid,omitempty"`
	ExitCode int      `json:"exit_code"`
	Output   string   `json:"output_file,omitempty"`
	Bytes    int      `json:"bytes,omitempty"`
	// Tail is the last of the output, stored with the record so a job can be
	// read from a machine that did not run it. The full log stays on the
	// machine that produced it: a build log can be tens of megabytes, and a
	// database is the wrong place for it. What travels is what you look at.
	Tail      string `json:"output_tail,omitempty"`
	Truncated bool   `json:"output_truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	// Thread and Agent record who started it, so a job found later can be
	// traced back to the conversation that wanted it.
	Thread     string `json:"thread,omitempty"`
	Agent      string `json:"agent,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// Done reports whether the job has stopped running.
func (j Job) Done() bool { return j.Status != JobRunning }

// Line renders the job for a human or a model in one line.
func (j Job) Line() string {
	s := fmt.Sprintf("%s %s (%s)", j.ID, strings.TrimSpace(j.Command+" "+strings.Join(j.Args, " ")), j.Status)
	if j.Done() {
		s += fmt.Sprintf(" exit %d", j.ExitCode)
	}
	return s
}

// JobsDir is where captured output lives. It sits beside the profile rather
// than in /tmp: output that a later run is expected to read must survive a
// reboot's tmp cleaning.
func JobsDir() string {
	if p := os.Getenv("AHUB_CONFIG"); p != "" {
		return filepath.Join(filepath.Dir(p), "jobs")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "jobs"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agents-hub", "jobs")
}

// CreateJob records a job before anything is spawned, so a crash between the
// two leaves a visible record rather than an orphan process nobody knows about.
func (h *Hub) CreateJob(j Job) (Job, error) {
	doc := map[string]any{
		"command": j.Command, "status": JobRunning,
		"started_at": now(), "created_at": now(),
	}
	if len(j.Args) > 0 {
		b, _ := json.Marshal(j.Args)
		doc["args"] = string(b)
	}
	for k, v := range map[string]string{"thread": j.Thread, "agent": j.Agent} {
		if v != "" {
			doc[k] = v
		}
	}
	rec, err := h.C.Create(RefJobs, doc)
	if err != nil {
		return Job{}, err
	}
	j.ID = rec.Str("id")
	j.Status = JobRunning
	j.Output = filepath.Join(JobsDir(), j.ID+".out")
	if _, err := h.C.Patch(RefJobs, j.ID, map[string]any{"output_file": j.Output}); err != nil {
		return j, err
	}
	return j, nil
}

func jobFrom(r bkn.Record) Job {
	j := Job{
		ID: r.Str("id"), Command: r.Str("command"), Status: r.Str("status"),
		PID: int(r.Num("pid")), ExitCode: int(r.Num("exit_code")),
		Output: r.Str("output_file"), Bytes: int(r.Num("bytes")),
		Tail: r.Str("output_tail"), Truncated: r["output_truncated"] == true,
		Error: r.Str("error"), Thread: r.Str("thread"), Agent: r.Str("agent"),
		StartedAt: r.Str("started_at"), FinishedAt: r.Str("finished_at"),
	}
	if raw := r.Str("args"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &j.Args)
	}
	return j
}

// GetJob loads one job.
func (h *Hub) GetJob(id string) (Job, error) {
	rec, err := h.C.Get(RefJobs, id)
	if err != nil {
		return Job{}, err
	}
	return jobFrom(rec), nil
}

// ListJobs returns recent jobs, newest first.
func (h *Hub) ListJobs(status string, limit int) ([]Job, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	recs, err := h.C.List(RefJobs, q)
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(recs))
	for _, r := range recs {
		out = append(out, jobFrom(r))
	}
	return out, nil
}

// UpdateJob merges fields into a job record.
func (h *Hub) UpdateJob(id string, fields map[string]any) error {
	_, err := h.C.Patch(RefJobs, id, fields)
	return err
}

// Spawn starts the supervisor for a recorded job.
//
// The child is this same binary re-invoked as `ahub job _exec <id>`, not the
// command itself. That is what makes an exit code survive: the command's
// parent has to outlive it to reap it, and the process that started the turn
// is about to exit. The supervisor also re-checks the allowlist, so a job
// record edited after the fact still cannot run something unpermitted.
func (h *Hub) Spawn(j Job) (Job, error) {
	self, err := os.Executable()
	if err != nil {
		return j, err
	}
	if err := os.MkdirAll(JobsDir(), 0o700); err != nil {
		return j, err
	}
	logf, err := os.OpenFile(filepath.Join(JobsDir(), j.ID+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return j, err
	}
	defer logf.Close()

	cmd := exec.Command(self, "job", "_exec", j.ID)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Stdin = nil
	// Setsid detaches it from this process group, so the supervisor is not
	// killed when the shell that started the turn goes away.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return j, err
	}
	j.PID = cmd.Process.Pid
	// Released rather than waited on: this process is about to exit, and the
	// supervisor is deliberately not its problem any more.
	if err := cmd.Process.Release(); err != nil {
		return j, err
	}
	return j, h.UpdateJob(j.ID, map[string]any{"pid": j.PID})
}

// ResolveJobCommand checks a recorded job against the current policy and
// returns the binary and the arguments to run.
func ResolveJobCommand(policy flow.ToolPolicy, j Job) (argv []string, bin string, err error) {
	full, err := policy.Resolve(j.Command, j.Args)
	if err != nil {
		return nil, "", err
	}
	return full[1:], full[0], nil
}

// StartJob validates, records and spawns in one step, so no caller can do two
// of the three and leave a half-started job behind.
func StartJob(h *Hub, policy flow.ToolPolicy, j Job, extra []string) (Job, error) {
	if _, err := policy.Resolve(j.Command, extra); err != nil {
		return Job{}, err
	}
	j.Args = extra
	created, err := h.CreateJob(j)
	if err != nil {
		return Job{}, err
	}
	return h.Spawn(created)
}

// MaxTail is how much of a job's output rides along with its record.
const MaxTail = 16 << 10

// ReadOutput returns a job's output, preferring the local file.
//
// The local file is complete; the stored tail is merely portable. Reading a
// job on the machine that ran it should not silently give you less than
// reading it there would, so the file wins whenever it is present -- and the
// caller is told which one it got.
func (h *Hub) ReadOutput(j Job, maxBytes int) (text string, local bool, err error) {
	if j.Output != "" {
		data, rerr := os.ReadFile(j.Output)
		if rerr == nil {
			if maxBytes > 0 && len(data) > maxBytes {
				// The tail, not the head: the end of a log is where the
				// failure is.
				return "[...truncated]\n" + string(data[len(data)-maxBytes:]), true, nil
			}
			return string(data), true, nil
		}
		if !os.IsNotExist(rerr) {
			return "", false, rerr
		}
	}
	// No file here: either another machine ran it, or the file has been
	// cleaned up. The recorded tail is what is left.
	if j.Truncated && j.Tail != "" {
		return "[...truncated; the full log is on the machine that ran this job]\n" + j.Tail, false, nil
	}
	return j.Tail, false, nil
}

// TailOf reads the last MaxTail bytes of a file, for storing with a record.
func TailOf(path string) (tail string, truncated bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(data) > MaxTail {
		return string(data[len(data)-MaxTail:]), true, nil
	}
	return string(data), false, nil
}

// StopJob signals a running job.
func (h *Hub) StopJob(j Job) error {
	if j.Done() {
		return nil
	}
	if j.PID > 0 {
		// The negative pid signals the whole process group, which is what
		// Setsid created: killing only the supervisor would orphan the
		// command it is waiting on.
		_ = syscall.Kill(-j.PID, syscall.SIGTERM)
	}
	return h.UpdateJob(j.ID, map[string]any{
		"status": JobStopped, "finished_at": now(),
	})
}

// WaitJob polls until the job finishes or the deadline passes.
//
// Polling rather than watching: the job may have been started by a different
// process, on a different day, and a poll against a record both can see needs
// no shared state at all.
func (h *Hub) WaitJob(id string, timeout time.Duration) (Job, error) {
	deadline := time.Now().Add(timeout)
	every := 500 * time.Millisecond
	for {
		j, err := h.GetJob(id)
		if err != nil || j.Done() || time.Now().After(deadline) {
			return j, err
		}
		time.Sleep(every)
		if every < 3*time.Second {
			every *= 2
		}
	}
}
