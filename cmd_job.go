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
	case "_exec":
		jobExec(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"job takes list, show, output, wait, stop, start")
	}
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
	text, err := h.ReadOutput(j, fl.intVal("bytes", 8000))
	if err != nil {
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	out.Data(map[string]any{"job": j.ID, "status": j.Status,
		"exit_code": j.ExitCode, "output": text})
}

func jobWait(fl *flags) {
	h, _ := hubFor(fl)
	j := resolveJob(h, fl)
	done, err := h.WaitJob(j.ID, time.Duration(fl.intVal("timeout", 60))*time.Second)
	if err != nil {
		fail(err, "wait for job")
	}
	text, _ := h.ReadOutput(done, fl.intVal("bytes", 8000))
	fields := map[string]any{"job": done.ID, "status": done.Status,
		"exit_code": done.ExitCode, "output": text}
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
	prefixes, err := hub.ParsePrefixes(cfg.AllowToolCmds)
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

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
