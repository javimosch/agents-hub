package main

import (
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

func threadCmd(fl *flags) {
	h, _ := hubFor(fl)
	switch fl.arg(0) {
	case "", "list", "ls":
		threadList(h, fl)
	case "show", "get":
		threadShow(h, fl)
	case "rm", "delete":
		threadDelete(h, fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"thread takes list, show, rm")
	}
}

func threadList(h *hub.Hub, fl *flags) {
	list, err := h.ListThreads(fl.str("assistant"), fl.intVal("limit", 50))
	if err != nil {
		fail(err, "list threads")
	}
	out.Data(map[string]any{"count": len(list), "threads": list})
}

func threadShow(h *hub.Hub, fl *flags) {
	id := threadRef(fl)
	th, err := h.GetThread(id)
	if err != nil {
		fail(err, "load thread")
	}
	msgs, err := h.History(id, fl.intVal("limit", 0))
	if err != nil {
		fail(err, "load history")
	}
	out.Data(map[string]any{"thread": th, "count": len(msgs), "messages": msgs})
}

func threadDelete(h *hub.Hub, fl *flags) {
	id := threadRef(fl)
	n, err := h.DeleteThread(id)
	if err != nil {
		out.Log("deleted %d messages before failing", n)
		fail(err, "delete thread")
	}
	out.Data(map[string]any{"deleted": id, "messages_deleted": n})
}

func threadRef(fl *flags) string {
	if v := fl.str("thread"); v != "" {
		return v
	}
	if v := fl.arg(1); v != "" {
		return v
	}
	out.Fail(out.ValidationError, "validation_error", "name a thread", "ahub thread list")
	return ""
}
