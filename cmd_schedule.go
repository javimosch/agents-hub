package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/out"
	"github.com/javimosch/agents-hub/internal/provider"
)

func scheduleCmd(fl *flags) {
	switch fl.arg(0) {
	case "create":
		scheduleCreate(fl)
	case "", "list", "ls":
		scheduleList(fl)
	case "show", "get":
		scheduleShow(fl)
	case "rm", "delete":
		scheduleDelete(fl)
	case "enable":
		scheduleEnable(fl, true)
	case "disable":
		scheduleEnable(fl, false)
	case "run":
		scheduleRunOne(fl)
	case "due":
		scheduleDue(fl)
	case "tick":
		scheduleTick(fl)
	case "runs":
		scheduleRuns(fl)
	case "scratchpad":
		scheduleScratchpad(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"schedule takes create, list, show, rm, enable, disable, run, due, tick, runs, scratchpad")
	}
}

func scheduleCreate(fl *flags) {
	h, _ := hubFor(fl)
	every := 0
	if v := fl.str("every"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			out.Fail(out.InvalidValue, "invalid_value",
				"--every takes a duration of at least 1m, like 15m or 6h")
		}
		every = int(d.Seconds())
	}
	if err := hub.ValidateAt(fl.str("at")); err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error())
	}
	a, err := h.CreateSchedule(hub.Schedule{
		Name: fl.str("name"), Agent: fl.str("agent"), Prompt: fl.str("prompt"),
		EverySecs: every, At: fl.str("at"),
		Tools:         splitList(fl.str("tools")),
		Skills:        splitList(fl.str("skills")),
		MemoryProject: fl.str("memory-project"),
		MaxSteps:      fl.intVal("max-steps", 0),
		TimeoutSecs:   fl.intVal("timeout", 0),
	})
	if err != nil {
		if !isBackend(err) {
			out.Fail(out.ValidationError, "validation_error", err.Error(),
				`ahub schedule create --name nightly --agent helper --prompt "..." --every 24h`)
		}
		fail(err, "create schedule")
	}
	out.Data(map[string]any{"schedule": a})
}

func scheduleList(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListSchedules(fl.intVal("limit", 50))
	if err != nil {
		fail(err, "list schedules")
	}
	out.Data(map[string]any{"count": len(list), "schedules": list})
}

func scheduleShow(fl *flags) {
	h, _ := hubFor(fl)
	out.Data(map[string]any{"schedule": resolveSchedule(h, scheduleRef(fl))})
}

func scheduleDelete(fl *flags) {
	h, _ := hubFor(fl)
	a := resolveSchedule(h, scheduleRef(fl))
	if err := h.DeleteSchedule(a.ID); err != nil {
		fail(err, "delete schedule")
	}
	out.Data(map[string]any{"deleted": a.ID, "name": a.Name})
}

func scheduleEnable(fl *flags, on bool) {
	h, _ := hubFor(fl)
	a := resolveSchedule(h, scheduleRef(fl))
	fields := map[string]any{"enabled": on}
	// Re-arming a disabled agent computes a fresh due time, so it does not
	// fire immediately for every slot it slept through.
	if on {
		if next, err := a.NextRun(time.Now().UTC()); err == nil && !next.IsZero() {
			fields["next_run_at"] = next.Format(time.RFC3339)
		}
	}
	updated, err := h.UpdateSchedule(a.ID, fields)
	if err != nil {
		fail(err, "update schedule")
	}
	out.Data(map[string]any{"schedule": updated})
}

func scheduleDue(fl *flags) {
	h, _ := hubFor(fl)
	due, err := h.DueSchedules(time.Now().UTC())
	if err != nil {
		fail(err, "list due schedules")
	}
	names := make([]string, 0, len(due))
	for _, a := range due {
		names = append(names, a.Name)
	}
	out.Data(map[string]any{"count": len(due), "due": names, "at": time.Now().UTC().Format(time.RFC3339)})
}

// scheduleTick runs everything that is due. This is what a timer calls: one
// entry point, however many agents, and no daemon holding state between them.
func scheduleTick(fl *flags) {
	h, cfg := hubFor(fl)
	// Jobs first: a supervisor that died leaves a record saying "running"
	// forever, and a tick is the only thing that comes round regularly enough
	// to notice.
	if reaped, rerr := h.ReapJobs(time.Minute); rerr == nil && reaped > 0 {
		out.Log("marked %d job(s) failed whose supervisor had gone", reaped)
	}

	due, err := h.DueSchedules(time.Now().UTC())
	if err != nil {
		fail(err, "list due schedules")
	}
	results := []map[string]any{}
	failed, skipped := 0, 0
	owner := hub.Owner()
	for _, a := range due {
		// One run at a time per schedule. A five-minute timer and a
		// four-minute agent overlap on the fifth minute otherwise, and the
		// two runs would race on the same scratchpad.
		lease, lerr := h.AcquireLease(a.ID, owner, time.Duration(fl.intVal("lease-ttl", 1800))*time.Second)
		if lerr != nil {
			fail(lerr, "take a lease on "+a.Name)
		}
		if !lease.Held {
			skipped++
			results = append(results, map[string]any{
				"schedule": a.Name, "status": "skipped",
				"held_by": lease.HeldBy, "lease_expires": lease.Expires.UTC().Format(time.RFC3339),
			})
			continue
		}
		run := executeSchedule(h, cfg, fl, a, "schedule", &lease)
		if rerr := h.ReleaseLease(a.ID, lease); rerr != nil {
			out.Log("ran %s but could not release its lease: %v", a.Name, rerr)
		}
		if run.Status != "ok" {
			failed++
		}
		results = append(results, map[string]any{
			"schedule": a.Name, "status": run.Status, "run": run.ID,
			"steps": run.Steps, "tokens": run.Tokens, "error": run.Error,
		})
	}
	fields := map[string]any{"ran": len(results) - skipped, "failed": failed,
		"skipped": skipped, "runs": results}
	if failed > 0 {
		// A timer that always exits 0 is a timer nobody looks at. One failed
		// agent makes the whole tick visible in `systemctl --failed`, while
		// the successful ones have already been recorded.
		b, _ := json.Marshal(fields)
		out.Fail(out.ExternalError, "schedule_run_failed",
			pluralSchedules(failed)+" failed", string(b))
	}
	out.Data(fields)
}

func scheduleRunOne(fl *flags) {
	h, cfg := hubFor(fl)
	a := resolveSchedule(h, scheduleRef(fl))
	// A manual run takes the same lease, so `schedule run` while a timer is
	// mid-run waits its turn rather than doubling up.
	lease, lerr := h.AcquireLease(a.ID, hub.Owner(), time.Duration(fl.intVal("lease-ttl", 1800))*time.Second)
	if lerr != nil {
		fail(lerr, "take a lease")
	}
	if !lease.Held {
		out.Fail(out.Conflict, "already_running",
			"schedule "+a.Name+" is already running, held by "+lease.HeldBy,
			"wait for it, or `ahub schedule show "+a.Name+"` to see the lease")
	}
	run := executeSchedule(h, cfg, fl, a, "manual", &lease)
	if rerr := h.ReleaseLease(a.ID, lease); rerr != nil {
		out.Log("could not release the lease: %v", rerr)
	}
	fields := map[string]any{
		"run": run.ID, "schedule": run.Schedule, "status": run.Status,
		"steps": run.Steps, "tokens": run.Tokens, "thread": run.Thread,
		"answer": run.Answer, "tool_runs": run.ToolRuns,
		"scratchpad_written": run.Wrote, "duration_ms": run.Duration,
	}
	if run.Status != "ok" {
		b, _ := json.Marshal(fields)
		out.Fail(out.ExternalError, "schedule_run_failed", run.Error, string(b))
	}
	out.Data(fields)
}

// executeSchedule runs one schedule and records what happened. Nothing here
// may exit: the caller holds the lease, and an exit would leave it held with no
// run recorded.
func executeSchedule(h *hub.Hub, cfg Config, fl *flags, a hub.Schedule, trigger string, lease *hub.Lease) *hub.ScheduleRun {
	opts := hub.RunScheduleOptions{Schedule: a, Trigger: trigger,
		KeepAlive: func() error { return h.RenewLease(lease) },
		Compact:   compactionFor(fl, cfg, h), DailyBudget: cfg.DailyTokenBudget}
	opts.SetupError = setUpSchedule(h, cfg, fl, a, &opts)
	run, err := h.RunSchedule(context.Background(), opts)
	if err != nil {
		fail(err, "record schedule run")
	}
	return run
}

// setUpSchedule wires the provider, tools, memory and skills for one run.
func setUpSchedule(h *hub.Hub, cfg Config, fl *flags, a hub.Schedule, opts *hub.RunScheduleOptions) error {
	p, err := provider.New(cfg.Provider, provider.Config{
		BaseURL: cfg.ProviderURL, APIKey: cfg.ProviderKey, Timeout: cfg.providerTimeout(),
	})
	if err != nil {
		return err
	}
	opts.Provider = p

	// memgraph is needed only by the tools that use it. Memory and skills
	// degrade without it; a schedule that asked for neither must still run on
	// a machine that has never heard of it.
	var mc *memory.Client
	if len(a.Tools) > 0 || len(a.Skills) > 0 || a.MemoryProject != "" {
		c, err := memory.New(0)
		switch {
		case err == nil:
			mc = c
		case needsMemory(a.Tools):
			return fmt.Errorf("this schedule has memgraph-backed tools: %w", err)
		case len(a.Skills) > 0 || a.MemoryProject != "":
			out.Log("memory/skills: %v; running without them", err)
		}
	}
	if mc != nil && a.MemoryProject != "" {
		opts.Memory = &hub.MemoryOptions{
			Client: mc, Project: a.MemoryProject, Limit: fl.intVal("memory-limit", 5),
		}
	}
	if mc != nil && len(a.Skills) > 0 {
		opts.Skills = &hub.SkillOptions{Client: mc}
	}
	if len(a.Tools) > 0 {
		reg, err := hub.NewRegistry(hub.RegistryOptions{
			Names: a.Tools, Memory: mc, Project: a.MemoryProject,
			AllowWrites: fl.has("allow-writes"),
			Policy:      cfg.toolPolicy(), Timeout: cfg.timeout(),
			Hub: h,
		})
		if err != nil {
			return err
		}
		opts.Registry = reg
	}
	return nil
}

func scheduleRuns(fl *flags) {
	h, _ := hubFor(fl)
	list, err := h.ListScheduleRuns(fl.str("schedule"), fl.str("status"), fl.intVal("limit", 20))
	if err != nil {
		fail(err, "list schedule runs")
	}
	out.Data(map[string]any{"count": len(list), "runs": list})
}

// scheduleScratchpad reads or replaces the note an agent carries between runs.
// A human editing it is a supported operation: it is the one piece of an
// autonomous agent's state that is meant to be readable and correctable.
func scheduleScratchpad(fl *flags) {
	h, _ := hubFor(fl)
	a := resolveSchedule(h, scheduleRef(fl))
	switch {
	case fl.has("clear"):
		if err := h.WriteScratchpad(a, ""); err != nil {
			fail(err, "clear scratchpad")
		}
		out.Data(map[string]any{"schedule": a.Name, "cleared": true})
	case fl.str("set") != "":
		if err := h.WriteScratchpad(a, fl.str("set")); err != nil {
			fail(err, "write scratchpad")
		}
		out.Data(map[string]any{"schedule": a.Name, "written": true})
	default:
		out.Data(map[string]any{
			"schedule": a.Name, "scratchpad": a.Scratchpad,
			"chars": len(a.Scratchpad), "updated_at": a.ScratchpadAt,
		})
	}
}

func scheduleRef(fl *flags) string {
	if v := fl.str("agent"); v != "" {
		return v
	}
	return fl.arg(1)
}

func resolveSchedule(h *hub.Hub, ref string) hub.Schedule {
	if ref == "" {
		out.Fail(out.ValidationError, "validation_error", "name a schedule", "ahub schedule list")
	}
	a, err := h.FindSchedule(ref)
	if err != nil {
		if !isBackend(err) || bkn.NotFound(err) {
			out.Fail(out.NotFound, "not_found", err.Error(), "ahub schedule list")
		}
		fail(err, "resolve schedule")
	}
	return a
}

func pluralSchedules(n int) string {
	if n == 1 {
		return "1 schedule"
	}
	return fmt.Sprintf("%d schedules", n)
}
