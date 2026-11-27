package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/javimosch/agents-hub/internal/provider"
)

// runTool executes one call. A tool failure is returned to the model as the
// tool's result rather than raised: the model asked a question, "that did not
// work, here is why" is an answer it can act on, and killing the turn throws
// away everything the earlier steps established.
func (h *Hub) runTool(ctx context.Context, reg *Registry, step int, call provider.ToolCall) (string, ToolRun) {
	run := ToolRun{Step: step, Name: call.Name, Args: call.Arguments}
	t, ok := reg.get(call.Name)
	if !ok {
		// Naming the alternatives is what lets a model correct itself. Told
		// only "no such tool", a real one invented the same wrong name three
		// times and burned the whole step budget doing it.
		run.Failed = "no such tool"
		return fmt.Sprintf("Error: no tool named %q. Available tools: %s.",
			call.Name, strings.Join(reg.Names(), ", ")), run
	}
	args := json.RawMessage(call.Arguments)
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		run.Failed = "arguments were not valid JSON"
		return "Error: your arguments were not valid JSON. Send a JSON object.", run
	}
	// Checked here rather than left to the tool, so the model is told which
	// parameter it missed instead of receiving whatever error the tool
	// happens to produce when handed an empty string.
	if missing := missingArgs(t, args); missing != "" {
		run.Failed = "missing required parameter " + missing
		return fmt.Sprintf("Error: tool %q requires the parameter %q. Send it and try again.",
			call.Name, missing), run
	}
	result, err := t.Run(ctx, args)
	if err != nil {
		run.Failed = err.Error()
		return "Error: " + err.Error(), run
	}
	run.Chars = len(result)
	return result, run
}

// missingArgs reports the first required parameter the model left out.
func missingArgs(t Tool, args json.RawMessage) string {
	var got map[string]any
	if err := json.Unmarshal(args, &got); err != nil {
		return ""
	}
	for _, name := range t.Required() {
		v, present := got[name]
		if !present {
			return name
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			return name
		}
	}
	return ""
}

// answerUnrun stores a result for each call that will never run. A stored call
// with no result makes the thread unreplayable: providers reject it.
func (h *Hub) answerUnrun(th *Thread, seq int, calls []provider.ToolCall, why string) error {
	for _, call := range calls {
		if _, err := h.AppendMessage(Message{
			ThreadID: th.ID, Role: provider.RoleTool, Seq: seq,
			Content:    "Not run: " + why + ".",
			ToolCallID: call.ID, Name: call.Name,
		}); err != nil {
			return err
		}
		seq++
	}
	return h.touch(th, seq)
}
