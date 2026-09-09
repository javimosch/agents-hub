package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// ToolCatalog describes every tool this build knows about, whether or not it
// is enabled. It exists so an agent can discover the set without guessing.
func ToolCatalog() []map[string]any {
	all := builtins(nil, "")
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
			"writes": t.Writes, "needs_memgraph": true,
		})
	}
	return out
}

// NewRegistry selects tools by name.
//
// Every tool here is backed by memgraph, which is deliberate: the first tools
// an agent gets are the ones that let it look things up rather than the
// ones that let it act. A shell tool is not an oversight.
func NewRegistry(names []string, mc *memory.Client, defaultProject string, allowWrites bool) (*Registry, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if mc == nil {
		return nil, fmt.Errorf("tools need memgraph: %w", memory.ErrUnavailable)
	}
	all := builtins(mc, defaultProject)
	reg := &Registry{tools: map[string]Tool{}}
	for _, n := range names {
		t, ok := all[n]
		if !ok {
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
