package main

import (
	"encoding/json"
	"errors"

	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/out"
)

// memFail sorts a memgraph problem into the right exit code. A value this
// tool refused to pass on is the caller's to fix (85); anything else came
// from memgraph and may be worth retrying (105).
func memFail(err error) {
	if errors.Is(err, memory.ErrInvalidValue) {
		out.Fail(out.InvalidValue, "invalid_value", err.Error())
	}
	out.Fail(out.ExternalError, "memgraph_error", err.Error())
}

// memoryCmd drives memgraph directly. These commands need no bkn token: they
// touch a local store, not the hub's, and requiring a login to read your own
// notes would be theatre.
func memoryCmd(fl *flags) {
	switch fl.arg(0) {
	case "status":
		memoryStatus(fl)
	case "", "recall", "search":
		memoryRecall(fl)
	case "save", "remember":
		memorySave(fl)
	case "read":
		memoryRead(fl)
	case "projects":
		memoryProjects(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"memory takes status, recall, save, read, projects")
	}
}

// mem builds the client, turning a missing binary into a precondition error
// with the one instruction that fixes it.
func mem(fl *flags) *memory.Client {
	cfg, _, err := resolve(fl)
	if err != nil {
		out.Fail(out.ValidationError, "config_error", err.Error())
	}
	c, err := memory.New(cfg.timeout())
	if err != nil {
		if errors.Is(err, memory.ErrUnavailable) {
			out.Fail(out.NotFound, "memgraph_missing", err.Error(),
				"install memgraph, or point AHUB_MEMGRAPH at the binary")
		}
		out.Fail(out.ValidationError, "validation_error", err.Error())
	}
	return c
}

func memoryStatus(fl *flags) {
	bin, err := memory.Find()
	if err != nil {
		out.Fail(out.NotFound, "memgraph_missing", err.Error(),
			"install memgraph, or point AHUB_MEMGRAPH at the binary")
	}
	out.Data(map[string]any{"available": true, "binary": bin})
}

func memoryRecall(fl *flags) {
	q := fl.str("query")
	if q == "" {
		q = joinRest(fl, 1)
	}
	if q == "" {
		out.Fail(out.ValidationError, "validation_error", "--query is required",
			`ahub memory recall --query "how does the seeder release its port" --project essaim`)
	}
	res, err := mem(fl).Recall(memory.Query{
		Text:    q,
		Project: fl.str("project"),
		Tags:    fl.str("tags"),
		Limit:   fl.intVal("limit", 5),
		Format:  fl.str("format"),
	})
	if err != nil {
		memFail(err)
	}
	// Rendered through the same budget the chat path uses, so what an agent
	// reads here is what a model would have been shown.
	block := memory.Context(res, fl.str("project"), memory.Budget{
		PerMemory: fl.intVal("per-memory", 0),
		Total:     fl.intVal("chars", 0),
	})
	out.Data(map[string]any{
		"query": q, "project": fl.str("project"),
		"count": res.Count, "results": res.Results,
		"context": map[string]any{
			"used": block.Used, "chars": block.Chars, "trimmed": block.Trimmed,
		},
	})
}

func memorySave(fl *flags) {
	text := fl.str("text")
	if text == "" {
		text = joinRest(fl, 1)
	}
	if text == "" {
		out.Fail(out.ValidationError, "validation_error", "--text is required")
	}
	raw, err := mem(fl).Save(text, fl.str("project"), fl.str("type"), fl.str("tags"))
	if err != nil {
		memFail(err)
	}
	out.Data(map[string]any{"saved": passthrough(raw), "project": fl.str("project")})
}

func memoryRead(fl *flags) {
	ref := fl.arg(1)
	if ref == "" {
		ref = fl.str("id")
	}
	if ref == "" {
		out.Fail(out.ValidationError, "validation_error",
			"name a memory id or id/slug", "ahub memory recall --query ...")
	}
	raw, err := mem(fl).Read(ref, fl.str("project"))
	if err != nil {
		memFail(err)
	}
	out.Data(map[string]any{"memory": passthrough(raw)})
}

func memoryProjects(fl *flags) {
	raw, err := mem(fl).Projects()
	if err != nil {
		memFail(err)
	}
	out.Data(map[string]any{"projects": passthrough(raw)})
}

// passthrough forwards memgraph's own JSON when it is JSON, and its text
// otherwise. Re-modelling every shape memgraph emits would mean this tool
// breaking every time that one grows a field.
func passthrough(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		return v
	}
	return string(raw)
}

func joinRest(fl *flags, from int) string {
	if len(fl.rest) <= from {
		return ""
	}
	s := ""
	for i, w := range fl.rest[from:] {
		if i > 0 {
			s += " "
		}
		s += w
	}
	return s
}
