package main

import (
	"strings"
	"testing"

	"github.com/javimosch/agents-hub/internal/guide"
)

func nonEmptyStrings(v any) bool {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return false
	}
	for _, it := range items {
		if s, ok := it.(string); !ok || s == "" {
			return false
		}
	}
	return true
}

// The shape is cli-guide-spec's guide.schema.json.
func TestGuideHasTheShapeOfTheSpec(t *testing.T) {
	body, err := guide.Body("v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ahub", "one_liner"} {
		if s, ok := body[key].(string); !ok || s == "" {
			t.Errorf("%s: want a non-empty string", key)
		}
	}
	for _, key := range []string{"model", "concepts", "commands"} {
		if m, ok := body[key].(map[string]any); !ok || len(m) == 0 {
			t.Errorf("%s: want a non-empty object", key)
		}
	}
	for _, key := range []string{"loop", "gotchas", "see_also"} {
		if !nonEmptyStrings(body[key]) {
			t.Errorf("%s: want a list of strings", key)
		}
	}
	groups, _ := body["commands"].(map[string]any)
	for group, v := range groups {
		if !nonEmptyStrings(v) {
			t.Errorf("commands.%s: want a list of usage strings", group)
		}
	}
	examples, _ := body["examples"].([]any)
	if len(examples) == 0 {
		t.Error("examples: want a non-empty list")
	}
	for i, e := range examples {
		m, _ := e.(map[string]any)
		if goal, _ := m["goal"].(string); goal == "" || !nonEmptyStrings(m["do"]) {
			t.Errorf("examples[%d]: want a goal and a list of commands, got %v", i, e)
		}
	}
}

// An agent learns the tool from the guide, so every command must be in it.
func TestGuideCommandsCoverTheCatalog(t *testing.T) {
	body, err := guide.Body("v1")
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	groups, _ := body["commands"].(map[string]any)
	for _, v := range groups {
		items, _ := v.([]any)
		for _, it := range items {
			s, _ := it.(string)
			all = append(all, s+" ")
		}
	}
	joined := strings.Join(all, "\n")
	for name := range catalog()["commands"].(map[string]any) {
		if !strings.Contains(joined, "ahub "+name+" ") {
			t.Errorf("guide.commands does not show `ahub %s`", name)
		}
	}
}

func TestHumanGuideRendersEverySection(t *testing.T) {
	text, err := guide.Human("v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, heading := range []string{"## Model", "## The loop", "## Commands", "## Examples", "## Concepts", "## Gotchas"} {
		if !strings.Contains(text, heading) {
			t.Errorf("--human lacks %q", heading)
		}
	}
}
