package main

import (
	"errors"
	"strings"

	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/out"
)

// skillCmd browses memgraph's skill graph. Like memory, it needs no bkn
// token: the skills are on this machine.
func skillCmd(fl *flags) {
	switch fl.arg(0) {
	case "", "search", "list":
		skillSearch(fl)
	case "recommend":
		skillRecommend(fl)
	case "show", "get":
		skillShow(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"skill takes search, recommend, show")
	}
}

func skillSearch(fl *flags) {
	q := fl.str("query")
	if q == "" {
		q = joinRest(fl, 1)
	}
	if q == "" {
		out.Fail(out.ValidationError, "validation_error", "--query is required",
			`ahub skill search "cli output spec"`)
	}
	hits, err := mem(fl).SearchSkills(q, fl.intVal("limit", 10))
	if err != nil {
		memFail(err)
	}
	out.Data(map[string]any{"query": q, "count": len(hits), "skills": hits})
}

func skillRecommend(fl *flags) {
	task := fl.str("task")
	if task == "" {
		task = joinRest(fl, 1)
	}
	if task == "" {
		out.Fail(out.ValidationError, "validation_error", "describe the task",
			`ahub skill recommend "generate a changelog from git history"`)
	}
	hits, err := mem(fl).Recommend(task, fl.intVal("limit", 5))
	if err != nil {
		memFail(err)
	}
	out.Data(map[string]any{"task": task, "count": len(hits), "skills": hits})
}

// skillShow loads exactly what a chat turn would load, so an agent can read
// the procedure before deciding to spend tokens on it.
func skillShow(fl *flags) {
	name := fl.arg(1)
	if name == "" {
		name = fl.str("name")
	}
	if name == "" {
		out.Fail(out.ValidationError, "validation_error", "name a skill",
			`ahub skill search "..."`)
	}
	c := mem(fl)
	sk, err := c.FindSkill(name, fl.intVal("limit", 10))
	if err != nil {
		if errors.Is(err, memory.ErrInvalidValue) {
			out.Fail(out.NotFound, "not_found", err.Error(), `ahub skill search "`+name+`"`)
		}
		memFail(err)
	}
	body, err := memory.LoadSkill(sk)
	if err != nil {
		memFail(err)
	}
	out.Data(map[string]any{"skill": sk, "chars": len(body), "content": body})
}

// skillsFor decides which skills this turn loads.
func skillsFor(fl *flags, a hub.Agent) *hub.SkillOptions {
	if fl.has("no-skills") {
		return nil
	}
	named := splitList(fl.str("skill"))
	if len(named) == 0 && len(a.Skills) == 0 {
		return nil
	}
	c, err := memory.New(0)
	if err != nil {
		// Same rule as memory: explicit on the command line is an error,
		// configured on the agent degrades with a note on stderr.
		if len(named) > 0 {
			out.Fail(out.NotFound, "memgraph_missing", err.Error(),
				"install memgraph, point AHUB_MEMGRAPH at it, or pass --no-skills")
		}
		out.Log("skills: %v; answering without them", err)
		return nil
	}
	return &hub.SkillOptions{Client: c, Names: named, Required: fl.has("skills-required")}
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
