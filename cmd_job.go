package main

import (
	"context"
	"os"
	"time"

	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

func jobCmd(fl *flags) {
	switch fl.arg(0) {
	case "", "list", "ls":
		jobList(fl)
	case "show", "status", "get":
		jobShow(fl)
	case "output", "logs":
		jobOutput(fl)
	case "wait":
		jobWait(fl)
	case "stop", "kill":
		jobStop(fl)
	case "start", "run":
		jobStart(fl)
	case "prune":
		jobPrune(fl)
	case "reap":
		jobReap(fl)
	case "_exec":
		jobExec(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"job takes list, show, output, wait, stop, start")
	}
}

// jobPrune removes finished jobs and their output. A tool that budgets other
// people's disks should not quietly fill its own.
func jobPrune(fl *flags) {
	h, cfg := hubFor(fl)
	days := fl.intVal("older-than-days", cfg.JobRetentionDays)
	if days <= 0 {
		days = 14
	}
	jobs, bytes, err := h.PruneJobs(time.Duration(days)*24*time.Hour, fl.has("dry-run"))
	if err != nil {
		fail(err, "prune jobs")
	}
	out.Data(map[string]any{
		"pruned": jobs, "bytes_freed": bytes,
		"older_than_days": days, "dry_run": fl.has("dry-run"),
	})
}

// jobReap marks jobs whose supervisor is gone, so nothing says "running"
// forever because a process was killed.
func jobReap(fl *flags) {
	h, _ := hubFor(fl)
	n, err := h.ReapJobs(time.Duration(fl.intVal("grace", 60)) * time.Second)
	if err != nil {
		fail(err, "reap jobs")
	}
	out.Data(map[string]any{"reaped": n})
}

func jobList(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListJobs(fl.str("status"), fl.intVal("limit", 20))
	if err != nil {
		fail(err, "list jobs")
	}
	out.Data(map[string]any{"count": len(list), "jobs": list})
}

func jobShow(fl *flags) {
	h, _ := hubFor(fl)
	out.Data(map[string]any{"job": resolveJob(h, fl)})
}

func jobOutput(fl *flags) {
	h, _ := hubFor(fl)
	j := resolveJob(h, fl)
	text, local, err := h.ReadOutput(j, fl.intVal("bytes", 8000))
	if err != nil {
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	out.Data(map[string]any{"job": j.ID, "status": j.Status,
		"exit_code": j.ExitCode, "output": text,
		// Which copy this is, because "complete" and "portable" are not the
		// same thing and a reader should not have to guess.
		"source": sourceOf(local)})
}

func jobWait(fl *flags) {
	h, _ := hubFor(fl)
	j := resolveJob(h, fl)
	done, err := h.WaitJob(j.ID, time.Duration(fl.intVal("timeout", 60))*time.Second)
	if err != nil {
		fail(err, "wait for job")
	}
	text, local, _ := h.ReadOutput(done, fl.intVal("bytes", 8000))
	fields := map[string]any{"job": done.ID, "status": done.Status,
		"exit_code": done.ExitCode, "output": text, "source": sourceOf(local)}
	if !done.Done() {
		// Still running is not a failure, but a caller must be able to tell
		// "it finished" from "I stopped waiting" without reading the payload.
		b, _ := jsonBytes(fields)
		out.Fail(out.NotFound, "still_running",
			"job "+done.ID+" is still running", "ahub job wait "+done.ID+" --timeout 300", b)
	}
	out.Data(fields)
}

func jobStop(fl *flags) {
	h, _ := hubFor(fl)
	j := resolveJob(h, fl)
	if err := h.StopJob(j); err != nil {
		fail(err, "stop job")
	}
	out.Data(map[string]any{"job": j.ID, "stopped": true})
}

// jobStart launches a job from the command line, using the same allowlist the
// cmd tool uses -- a human starting one by hand is still bound by what this
// machine permits a model to run.
func jobStart(fl *flags) {
	h, cfg := hubFor(fl)
	command := fl.str("command")
	if command == "" {
		command = joinRest(fl, 1)
	}
	if command == "" {
		out.Fail(out.ValidationError, "validation_error",
			`give a command: ahub job start --command "gh pr list"`)
	}
	j, err := hub.StartJob(h, cfg.AllowToolCmds, hub.Job{Command: command}, fl.list("args"))
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error(),
			"ahub config set --allow-tool-cmd '<prefix>'")
	}
	out.Data(map[string]any{"job": j})
}

// jobExec is the supervisor: it runs the recorded command in the foreground,
// captures its output, and writes the result back. It is hidden from the
// catalog because nothing should call it but Spawn.
func jobExec(fl *flags) {
	h, cfg := hubFor(fl)
	id := fl.arg(1)
	if id == "" {
		out.Fail(out.ValidationError, "validation_error", "job _exec needs a job id")
	}
	j, err := h.GetJob(id)
	if err != nil {
		fail(err, "load job")
	}
	// Re-checked here, against this machine's current allowlist, because the
	// record has been out of this process's hands since it was written.
	prefixes, err := flow.ParsePrefixes(cfg.AllowToolCmds)
	if err != nil {
		finishJob(h, j, 0, err)
		return
	}
	argv, bin, perr := hub.ResolveJobCommand(prefixes, j)
	if perr != nil {
		finishJob(h, j, 0, perr)
		return
	}

	f, err := os.OpenFile(j.Output, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		finishJob(h, j, 0, err)
		return
	}
	defer f.Close()

	timeout := time.Duration(fl.intVal("timeout", cfg.JobTimeoutSecs)) * time.Second
	res, runErr := flow.RunCmdTo(context.Background(),
		flow.Limits{Commands: []string{bin}, Timeout: timeout}, bin, argv, f)
	if runErr != nil {
		finishJob(h, j, 0, runErr)
		return
	}
	finishJob(h, j, res, nil)
}

func finishJob(h *hub.Hub, j hub.Job, exitCode int, err error) {
	fields := map[string]any{
		"status": hub.JobExited, "exit_code": exitCode, "finished_at": nowRFC(),
	}
	if st, serr := os.Stat(j.Output); serr == nil {
		fields["bytes"] = st.Size()
	}
	// The tail travels with the record so the job can be read from anywhere,
	// which is the whole point of recording it in bkn rather than only on disk.
	if tail, truncated, terr := hub.TailOf(j.Output); terr == nil && tail != "" {
		fields["output_tail"] = tail
		fields["output_truncated"] = truncated
	}
	if err != nil {
		fields["status"] = hub.JobFailed
		fields["error"] = err.Error()
	}
	if uerr := h.UpdateJob(j.ID, fields); uerr != nil {
		out.Log("job %s finished but could not be recorded: %v", j.ID, uerr)
		os.Exit(out.ExternalError)
	}
	out.Data(map[string]any{"job": j.ID, "status": fields["status"], "exit_code": exitCode})
}

func resolveJob(h *hub.Hub, fl *flags) hub.Job {
	id := fl.str("job")
	if id == "" {
		id = fl.arg(1)
	}
	if id == "" {
		out.Fail(out.ValidationError, "validation_error", "name a job", "ahub job list")
	}
	j, err := h.GetJob(id)
	if err != nil {
		fail(err, "load job")
	}
	return j
}

func sourceOf(local bool) string {
	if local {
		return "local file (complete)"
	}
	return "recorded tail (portable)"
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
