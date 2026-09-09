// Package guide serves the embedded operator manual per cli-guide-spec v1.0.
// The guide travels inside the binary: it works offline, and it can never
// drift from the build it ships with.
package guide

import (
	_ "embed"
	"encoding/json"
	"fmt"
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
	list := func(title, key string) {
		items, ok := m[key].([]any)
		if !ok || len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", title)
		for _, it := range items {
			switch t := it.(type) {
			case string:
				fmt.Fprintf(&b, "- %s\n", t)
			case map[string]any:
				if c, ok := t["command"]; ok {
					fmt.Fprintf(&b, "- `%v`\n", c)
					if w, ok := t["why"]; ok {
						fmt.Fprintf(&b, "  %v\n", w)
					}
					continue
				}
				if n, ok := t["name"]; ok {
					fmt.Fprintf(&b, "- **%v** — %v\n", n, t["what"])
					continue
				}
				fmt.Fprintf(&b, "- %v\n", t)
			}
		}
		b.WriteString("\n")
	}
	list("The loop", "loop")
	list("Concepts", "concepts")
	list("Gotchas", "gotchas")
	list("Recipes", "recipes")
	return b.String(), nil
}
