package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is one entry in memgraph's skill graph.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Project     string   `json:"project,omitempty"`
	FilePath    string   `json:"file_path,omitempty"`
	Score       float64  `json:"score,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type recommendResult struct {
	Task        string  `json:"task"`
	Count       int     `json:"count"`
	Recommended []Skill `json:"recommended"`
}

type queryResult struct {
	Query   string  `json:"query"`
	Count   int     `json:"count"`
	Results []Skill `json:"results"`
}

// Recommend asks memgraph which skills fit a task description.
func (c *Client) Recommend(task string, limit int) ([]Skill, error) {
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("%w: recommend needs a task", ErrInvalidValue)
	}
	raw, err := c.run(withLimit([]string{"recommend", task, "--json", "--no-interactive"}, limit))
	if err != nil {
		return nil, err
	}
	var out recommendResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("memgraph recommend returned an unexpected shape: %w", err)
	}
	return out.Recommended, nil
}

// SearchSkills queries the skill graph by text.
func (c *Client) SearchSkills(q string, limit int) ([]Skill, error) {
	if strings.TrimSpace(q) == "" {
		return nil, fmt.Errorf("%w: search needs a query", ErrInvalidValue)
	}
	raw, err := c.run(withLimit([]string{"query", q, "--json", "--no-interactive"}, limit))
	if err != nil {
		return nil, err
	}
	var out queryResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("memgraph query returned an unexpected shape: %w", err)
	}
	return out.Results, nil
}

func withLimit(args []string, limit int) []string {
	if limit > 0 {
		return append(args, "--limit", fmt.Sprint(limit))
	}
	return args
}

// FindSkill resolves a name to exactly one skill. An ambiguous name is an
// error: loading the wrong instructions is worse than loading none.
func (c *Client) FindSkill(name string, limit int) (Skill, error) {
	hits, err := c.SearchSkills(name, limit)
	if err != nil {
		return Skill{}, err
	}
	for _, s := range hits {
		if strings.EqualFold(s.Name, name) || strings.EqualFold(s.ID, name) {
			return s, nil
		}
	}
	if len(hits) == 0 {
		return Skill{}, fmt.Errorf("%w: no skill named %q", ErrInvalidValue, name)
	}
	return Skill{}, fmt.Errorf("%w: no skill named exactly %q (closest: %s)",
		ErrInvalidValue, name, hits[0].Name)
}

// maxSkill caps how much of a SKILL.md is read. A skill is instructions, not
// a corpus; anything past this is a document that wandered into the wrong
// directory, and pasting it into every turn would be expensive and useless.
const maxSkill = 64 << 10

// LoadSkill reads a skill's file.
//
// The path is never taken from a user: it comes back from memgraph, which
// found it in a skills directory it was pointed at. This function still
// checks it, because "the path came from a trusted tool" is the sentence that
// precedes reading /etc/shadow when the tool is one day given a bad index.
func LoadSkill(s Skill) (string, error) {
	if s.FilePath == "" {
		return "", fmt.Errorf("%w: skill %q has no file", ErrInvalidValue, s.Name)
	}
	p := filepath.Clean(s.FilePath)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: skill path %q is not absolute", ErrInvalidValue, p)
	}
	if !strings.EqualFold(filepath.Ext(p), ".md") {
		return "", fmt.Errorf("%w: skill file %q is not markdown", ErrInvalidValue, p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: skill file %q is not a regular file", ErrInvalidValue, p)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, maxSkill)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	body := strings.TrimSpace(string(buf[:n]))
	if info.Size() > maxSkill {
		body += "\n[...truncated]"
	}
	return body, nil
}

// SkillBlock renders loaded skills as one system message.
//
// Skills are instructions on purpose -- that is what a skill is -- so unlike
// the memory block this one is not framed as inert reference material. The
// safety property is upstream: a skill is loaded only when it was named in
// the assistant's configuration or on the command line.
func SkillBlock(loaded map[string]string, order []string) string {
	if len(loaded) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Skills\n\nThe following procedures apply to this task.\n\n")
	for _, name := range order {
		body, ok := loaded[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, "### skill: %s\n%s\n\n", name, body)
	}
	return strings.TrimRight(sb.String(), "\n")
}
