package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/memory"
	"github.com/javimosch/agents-hub/internal/provider"
)

// Tool is something the model may ask to have run.
//
// Writes marks a tool that changes state somewhere. It is not advisory: the
// registry refuses to hand one out unless writes were explicitly allowed, so
// enabling a tool set can never quietly enable a mutation.
type Tool struct {
	Spec   provider.Tool
	Run    func(ctx context.Context, args json.RawMessage) (string, error)
	Writes bool
}

// Registry is the set of tools a turn may use.
type Registry struct {
	tools map[string]Tool
}

// Required lists a tool's mandatory parameters, read back off its own schema
// so the check and the advertisement can never disagree.
func (t Tool) Required() []string {
	req, _ := t.Spec.Parameters["required"].([]string)
	return req
}

// Names lists the registered tools, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Specs is what gets advertised to the model.
func (r *Registry) Specs() []provider.Tool {
	out := make([]provider.Tool, 0, len(r.tools))
	for _, n := range r.Names() {
		out = append(out, r.tools[n].Spec)
	}
	return out
}

// With returns a registry carrying one extra tool. The copy matters: the
// scratchpad tool is bound to one agent, and a shared registry would let a
// later run write into an earlier agent's note.
func (r *Registry) With(name string, t Tool) *Registry {
	out := &Registry{tools: map[string]Tool{}}
	if r != nil {
		for k, v := range r.tools {
			out.tools[k] = v
		}
	}
	out.tools[name] = t
	return out
}

func (r *Registry) get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Len reports how many tools are enabled.
func (r *Registry) Len() int { return len(r.tools) }

// needsMemgraph reports whether any named tool requires it, so an agent that
// only wants cmd can run on a machine with no memgraph at all.
func needsMemgraph(names []string) bool {
	for _, n := range names {
		if n != "cmd" && !strings.HasPrefix(n, "job_") {
			return true
		}
	}
	return false
}

// ToolCatalog describes every tool this build knows about, whether or not it
// is enabled. It exists so an agent can discover the set without guessing.
func ToolCatalog() []map[string]any {
	all := builtins(nil, "")
	all["cmd"] = cmdTool(flow.ToolPolicy{}, nil, 0)
	for n, t := range jobTools(nil, flow.ToolPolicy{}, nil) {
		all[n] = t
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		t := all[n]
		out = append(out, map[string]any{
			"name": n, "description": t.Spec.Description,
			"writes": t.Writes, "needs_memgraph": !strings.HasPrefix(n, "cmd") && !strings.HasPrefix(n, "job"),
			"needs_allowlist": n == "cmd" || strings.HasPrefix(n, "job"),
		})
	}
	return out
}

// RegistryOptions is what a turn may be given.
type RegistryOptions struct {
	Names   []string
	Memory  *memory.Client
	Project string
	// AllowWrites permits a tool that mutates a shared store.
	AllowWrites bool
	// Policy is what the cmd and job tools may run. No commands means those
	// tools do not exist.
	Policy  flow.ToolPolicy
	Timeout time.Duration
	// Hub and Thread are needed by the job tools, which record what they
	// start. A registry built without a Hub simply has no job tools.
	Hub *Hub
}

// threadKey carries the current thread through the context, because a turn on
// a NEW thread has no id until the turn is already under way -- and a job
// recorded without one cannot be traced back to what asked for it.
type ctxKey int

const threadKey ctxKey = iota

// WithThread marks a context as belonging to a thread.
func WithThread(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, threadKey, id)
}

// ThreadOf reads the thread a tool call belongs to, if any.
func ThreadOf(ctx context.Context) string {
	id, _ := ctx.Value(threadKey).(string)
	return id
}

// NewRegistry selects tools by name.
//
// Most tools here are backed by memgraph -- looking things up rather than
// acting -- and the one exception, cmd, can only run command shapes an
// operator enumerated in advance. There is still no shell tool, and that is
// not an oversight.
func NewRegistry(opts RegistryOptions) (*Registry, error) {
	names := opts.Names
	if len(names) == 0 {
		return nil, nil
	}
	mc, defaultProject, allowWrites := opts.Memory, opts.Project, opts.AllowWrites
	if mc == nil && needsMemgraph(names) {
		return nil, fmt.Errorf("tools need memgraph: %w", memory.ErrUnavailable)
	}
	all := builtins(mc, defaultProject)
	prefixes, err := flow.ParsePrefixes(opts.Policy.Commands)
	if err != nil {
		return nil, err
	}
	if len(prefixes) > 0 {
		all["cmd"] = cmdTool(opts.Policy, prefixes, opts.Timeout)
		if opts.Hub != nil {
			for n, t := range jobTools(opts.Hub, opts.Policy, prefixes) {
				all[n] = t
			}
		}
	}
	reg := &Registry{tools: map[string]Tool{}}
	for _, n := range names {
		t, ok := all[n]
		if !ok {
			if n == "cmd" {
				return nil, fmt.Errorf("the cmd tool has no permitted commands on this machine; " +
					"add one with `ahub config set --allow-tool-cmd 'gh pr list'`")
			}
			return nil, fmt.Errorf("unknown tool %q (known: %s)", n, strings.Join(catalogNames(all), ", "))
		}
		if t.Writes && !allowWrites {
			return nil, fmt.Errorf("tool %q writes; pass --allow-writes to enable it", n)
		}
		reg.tools[n] = t
	}
	return reg, nil
}

func catalogNames(all map[string]Tool) []string {
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func str(args json.RawMessage, key string) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func num(args json.RawMessage, key string, def int) int {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return def
	}
	if f, ok := m[key].(float64); ok && int(f) > 0 {
		return int(f)
	}
	return def
}

// builtins is the whole tool surface. A nil client is allowed so the catalog
// can be described without one.
// defaulted returns the argument if the model supplied one, else the turn's
// configured project.
//
// A model asked to search "the machin project" will often not repeat the
// project name in the arguments, because from where it sits that is context
// rather than a parameter. Without this the call fails and the model retries
// the same shape until the step cap -- which is exactly what it did.
func defaulted(args json.RawMessage, key, fallback string) string {
	if v := str(args, key); v != "" {
		return v
	}
	return fallback
}

func builtins(mc *memory.Client, defaultProject string) map[string]Tool {
	return map[string]Tool{
		"memory_recall": {
			Spec: provider.Tool{
				Name: "memory_recall",
				Description: "Search the memory store for notes written by past sessions. " +
					"Use this when the question concerns a specific project, decision or gotcha " +
					"that would not be general knowledge.",
				Parameters: object(map[string]any{
					"query":   prop("string", "What to search for."),
					"project": prop("string", "Memory project to search. Omit to use the agent's own project."),
					"limit":   prop("integer", "How many memories to return (default 5)."),
				}, "query"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				project := defaulted(args, "project", defaultProject)
				res, err := mc.Recall(memory.Query{
					Text:    str(args, "query"),
					Project: project,
					Limit:   num(args, "limit", 5),
					Format:  "index",
				})
				if err != nil {
					return "", err
				}
				// Rendered through the same budget as pre-turn recall: a tool
				// result is prompt too, and an unbudgeted one is worse
				// because the model can ask for it repeatedly.
				b := memory.Context(res, project, memory.DefaultBudget())
				if b.Used == 0 {
					return "No memories matched.", nil
				}
				return b.Text, nil
			},
		},
		"skill_search": {
			Spec: provider.Tool{
				Name: "skill_search",
				Description: "Find procedures (skills) by topic. Returns names and descriptions, " +
					"not the procedure itself; use skill_show to read one.",
				Parameters: object(map[string]any{
					"query": prop("string", "Topic to search for."),
					"limit": prop("integer", "How many skills to return (default 10)."),
				}, "query"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				hits, err := mc.SearchSkills(str(args, "query"), num(args, "limit", 10))
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return "No skills matched.", nil
				}
				var sb strings.Builder
				for _, s := range hits {
					fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
				}
				return strings.TrimSpace(sb.String()), nil
			},
		},
		"skill_show": {
			Spec: provider.Tool{
				Name:        "skill_show",
				Description: "Read one procedure (skill) in full, by exact name.",
				Parameters: object(map[string]any{
					"name": prop("string", "Exact skill name, as returned by skill_search."),
				}, "name"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				sk, err := mc.FindSkill(str(args, "name"), 10)
				if err != nil {
					return "", err
				}
				return memory.LoadSkill(sk)
			},
		},
		"memory_save": {
			Writes: true,
			Spec: provider.Tool{
				Name: "memory_save",
				Description: "Record a durable note for future sessions. Use only for things " +
					"that would cost real time to rediscover: decisions, gotchas, why something " +
					"was rejected. Never for what the code already says.",
				Parameters: object(map[string]any{
					"text":    prop("string", "The note. One fact, stated plainly."),
					"project": prop("string", "Memory project to write to."),
					"type":    prop("string", "One of user, feedback, project, reference."),
					"tags":    prop("string", "Comma-separated tags."),
				}, "text"),
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				raw, err := mc.Save(str(args, "text"), defaulted(args, "project", defaultProject),
					str(args, "type"), str(args, "tags"))
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(string(raw)), nil
			},
		},
	}
}

func object(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type": "object", "properties": props, "required": required,
	}
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}
