package main

import "github.com/javimosch/agents-hub/internal/hub"
import "github.com/javimosch/agents-hub/internal/out"

// toolCmd lists what a model can be given. It is a catalog, not a state: no
// backend, no memgraph, no token -- an agent should be able to find out what
// exists before it has any of those working.
func toolCmd(fl *flags) {
	switch fl.arg(0) {
	case "", "list", "ls":
		out.Data(map[string]any{"tools": hub.ToolCatalog(),
			"note": "tools are opt-in per assistant or per call; a tool that writes also needs --allow-writes"})
	default:
		out.Fail(out.InvalidArguments, "unknown_command", "tool takes list")
	}
}
