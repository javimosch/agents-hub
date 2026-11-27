package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/provider"
)

// maxCmdOutput caps what a command may hand back to a model. The result goes
// straight into the next prompt and is paid for on every subsequent step of
// the loop, so an uncapped `find /` is a bill as much as a mistake.
const maxCmdOutput = 4000

// cmdTool exposes the allowed prefixes to a model.
//
// There is no second --allow-writes gate here, and that is deliberate rather
// than an omission: the prefix list IS the gate, and it is far more specific
// than a blanket flag. It is empty by default, so the tool does not exist
// until an operator writes down exactly which command shapes are permitted --
// and a flag that must be passed even to run `git log` would only teach
// people that the flag means nothing.
func cmdTool(policy flow.ToolPolicy, prefixes []flow.Prefix, timeout time.Duration) Tool {
	allowed := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		allowed = append(allowed, p.Raw)
	}
	list := strings.Join(allowed, " | ")
	return Tool{
		Spec: provider.Tool{
			Name: "cmd",
			Description: "Run one of the commands this machine permits, and read its output. " +
				"You may never change a command, and may append only the arguments listed with it " +
				"(* stands for any text). Permitted commands: " + policy.Describe() + ". " +
				"The result carries the exit code: 0 succeeded, and every tool here follows the " +
				"same convention -- 105 means retry, 82 means your arguments were wrong, " +
				"92 means the thing was not found.",
			Parameters: object(map[string]any{
				"command": prop("string", "Exactly one of the permitted commands, copied verbatim: "+list),
				"args":    arrayProp("string", "Extra arguments to append, only as listed for the command. Each element is one argument; they are never re-parsed by a shell."),
			}, "command"),
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			want := str(args, "command")
			extra, err := strList(args, "args")
			if err != nil {
				return "", err
			}
			argv, err := policy.Resolve(want, extra)
			if err != nil {
				return "", err
			}
			out, err := flow.RunCmd(ctx,
				flow.Limits{Commands: []string{argv[0]}, Timeout: timeout},
				argv[0], argv[1:], "")
			if err != nil {
				return "", err
			}
			return renderCmdResult(out), nil
		},
	}
}

// renderCmdResult gives the model the exit code and both streams, trimmed.
// The code comes first because it is the part a model should branch on, and
// the part it will otherwise ignore in favour of guessing from the text.
func renderCmdResult(o *flow.Output) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %s\n", strconv.Itoa(o.Code))
	if s := trimTo(o.Text, maxCmdOutput); s != "" {
		fmt.Fprintf(&b, "stdout:\n%s\n", s)
	}
	if s := trimTo(o.Err, 1000); s != "" {
		fmt.Fprintf(&b, "stderr:\n%s\n", s)
	}
	if o.Text == "" && o.Err == "" {
		b.WriteString("(no output)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// jobTools are how a model starts work that outlives the turn.
//
// Three verbs and no more: start, check, read. A model that could also list
// and stop every job on the machine would be able to interfere with work it
// did not begin, and nothing it does here should reach outside its own turn.
func jobTools(h *Hub, policy flow.ToolPolicy, prefixes []flow.Prefix) map[string]Tool {
	allowed := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		allowed = append(allowed, p.Raw)
	}
	list := strings.Join(allowed, " | ")
	return map[string]Tool{
		"job_start": {
			Spec: provider.Tool{
				Name: "job_start",
				Description: "Start a permitted command in the background and get a job id back " +
					"immediately. Use this instead of cmd when the work takes more than a few " +
					"seconds -- a test suite, a build, a deploy. Permitted commands, with the only " +
					"arguments each may take: " + policy.Describe(),
				Parameters: object(map[string]any{
					"command": prop("string", "Exactly one of the permitted commands: "+list),
					"args":    arrayProp("string", "Extra arguments to append."),
				}, "command"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				extra, err := strList(args, "args")
				if err != nil {
					return "", err
				}
				j, err := StartJob(h, policy, Job{
					Command: str(args, "command"), Thread: ThreadOf(ctx),
				}, extra)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Started job %s. It is running in the background; "+
					"call job_wait with that id when you need the result.", j.ID), nil
			},
		},
		"job_wait": {
			Spec: provider.Tool{
				Name: "job_wait",
				Description: "Wait for a background job, up to a few seconds, and read its result. " +
					"If it is still running you get told so and can do something else and ask again -- " +
					"waiting is not the only thing you can do while work happens.",
				Parameters: object(map[string]any{
					"job":     prop("string", "The job id returned by job_start."),
					"seconds": prop("integer", "How long to wait before answering anyway (default 20, max 120)."),
				}, "job"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				secs := num(args, "seconds", 20)
				if secs > 120 {
					// A model that waits five minutes inside one tool call
					// spends the provider's timeout budget on doing nothing.
					secs = 120
				}
				j, err := h.WaitJob(str(args, "job"), time.Duration(secs)*time.Second)
				if err != nil {
					return "", err
				}
				if !j.Done() {
					return fmt.Sprintf("Job %s is still running after %ds. Do something else "+
						"and call job_wait again.", j.ID, secs), nil
				}
				text, _, err := h.ReadOutput(j, maxCmdOutput)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Job %s %s with exit code %d.\n%s",
					j.ID, j.Status, j.ExitCode, strings.TrimSpace(text)), nil
			},
		},
		"job_output": {
			Spec: provider.Tool{
				Name:        "job_output",
				Description: "Read the tail of a background job's output without waiting for it to finish.",
				Parameters: object(map[string]any{
					"job": prop("string", "The job id."),
				}, "job"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				j, err := h.GetJob(str(args, "job"))
				if err != nil {
					return "", err
				}
				text, _, err := h.ReadOutput(j, maxCmdOutput)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(text) == "" {
					return fmt.Sprintf("Job %s is %s and has produced no output yet.", j.ID, j.Status), nil
				}
				return fmt.Sprintf("Job %s (%s):\n%s", j.ID, j.Status, strings.TrimSpace(text)), nil
			},
		},
	}
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[...truncated]"
}

func strList(args json.RawMessage, key string) ([]string, error) {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, err
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		// A model that sends a single string instead of an array is making an
		// ordinary mistake, not an attack: one argument is a fine reading.
		if s, ok := raw.(string); ok {
			return []string{s}, nil
		}
		return nil, fmt.Errorf("args must be an array of strings")
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("every element of args must be a string")
		}
		out = append(out, s)
	}
	return out, nil
}

func arrayProp(itemType, desc string) map[string]any {
	return map[string]any{
		"type": "array", "description": desc,
		"items": map[string]any{"type": itemType},
	}
}
