// Package guide serves the embedded operator manual per cli-guide-spec v1.0.
// The guide travels inside the binary: it works offline, and it can never
// drift from the build it ships with.
package guide

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

//go:embed guide.json
var raw []byte

// Body returns the guide as a generic object, with the tool version injected
// so a reader can detect a stale build.
func Body(version string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["version"] = version
	return m, nil
}

// Human renders the guide as markdown for --human.
func Human(version string) (string, error) {
	m, err := Body(version)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# ahub %s\n\n%v\n\n", version, m["one_liner"])

	if model, ok := m["model"].(map[string]any); ok {
		b.WriteString("## Model\n\n")
		for _, k := range []string{"what_it_is", "where_state_lives", "what_it_is_not"} {
			if v, ok := model[k]; ok {
				fmt.Fprintf(&b, "- **%s** — %v\n", strings.ReplaceAll(k, "_", " "), v)
			}
		}
		b.WriteString("\n")
	}
	section(&b, "The loop", m["loop"])
	section(&b, "Commands", m["commands"])
	section(&b, "Examples", m["examples"])
	section(&b, "Concepts", m["concepts"])
	section(&b, "Gotchas", m["gotchas"])
	return b.String(), nil
}

// section renders one part of the guide: a list, a list of examples, or a
// keyed object whose values are text or lists of commands.
func section(b *strings.Builder, title string, v any) {
	fmt.Fprintf(b, "## %s\n\n", title)
	switch t := v.(type) {
	case []any:
		for _, it := range t {
			if example, ok := it.(map[string]any); ok {
				fmt.Fprintf(b, "- %v\n", example["goal"])
				steps, _ := example["do"].([]any)
				for _, step := range steps {
					fmt.Fprintf(b, "  `%v`\n", step)
				}
				continue
			}
			fmt.Fprintf(b, "- %v\n", it)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if commands, ok := t[k].([]any); ok {
				fmt.Fprintf(b, "- **%s**\n", k)
				for _, c := range commands {
					fmt.Fprintf(b, "  `%v`\n", c)
				}
				continue
			}
			fmt.Fprintf(b, "- **%s** — %v\n", k, t[k])
		}
	}
	b.WriteString("\n")
}
