// Package memory is a controlled wrapper around the memgraph CLI.
//
// agents-hub does not implement retrieval. memgraph already owns the memory
// store, its scoring and its taxonomy, and reimplementing any of that here
// would produce a second store to keep in sync with the first. This package
// shells out to the binary instead -- but "shells out" is exactly the phrase
// that should make a reader nervous, so the rules are narrow and enforced
// here rather than at each call site:
//
//   - argv is explicit; there is no shell, so nothing in a user's message can
//     be interpreted as a command, a pipe or a redirect.
//   - every value that reaches argv is validated, and a value that could be
//     read as a flag is rejected rather than escaped.
//   - every call has a timeout and a cap on how much output it will read. A
//     recall against a large project can emit hundreds of kilobytes, and an
//     unbounded read of that lands straight in a model's context window.
//   - the chat path uses read-only operations only. Writing a memory is an
//     explicit command a human or agent runs on purpose.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrUnavailable means the memgraph binary could not be found. It is a
// precondition failure, not a transient one: retrying will not install it.
var ErrUnavailable = errors.New("memgraph is not installed or not on PATH")

// ErrInvalidValue is a value this package refused to pass on. It is separate
// from a memgraph failure because the exit codes differ: one is the caller's
// mistake to fix, the other is worth a retry.
var ErrInvalidValue = errors.New("invalid value")

// maxOutput caps what a single call may return. memgraph's own recall can
// exceed 600 KB on a large project; anything past this is truncated rather
// than silently swallowed.
const maxOutput = 2 << 20 // 2 MiB

// nameRe is what a project, type or tag list may contain. It deliberately
// forbids a leading dash: a value that starts with one is a flag to the
// program being executed, whatever this code intended it to be.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// Client runs one memgraph binary.
type Client struct {
	Bin     string
	Timeout time.Duration
}

// Find locates the binary: AHUB_MEMGRAPH wins, then PATH, then the documented
// install location.
func Find() (string, error) {
	if p := os.Getenv("AHUB_MEMGRAPH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("AHUB_MEMGRAPH=%s: %w", p, err)
		}
		return p, nil
	}
	if p, err := exec.LookPath("memgraph"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "memgraph")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", ErrUnavailable
}

// New builds a client, reporting ErrUnavailable when there is no binary.
func New(timeout time.Duration) (*Client, error) {
	bin, err := Find()
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Client{Bin: bin, Timeout: timeout}, nil
}

// Result is one memory (or one section of one) that a recall matched.
type Result struct {
	MemoryID string  `json:"memory_id"`
	Score    float64 `json:"score"`
	Title    string  `json:"title"`
	Content  string  `json:"content"`
	Slug     string  `json:"slug,omitempty"`
}

// RecallResult is memgraph's answer to a search.
type RecallResult struct {
	Count   int      `json:"count"`
	Format  string   `json:"format"`
	Results []Result `json:"results"`
}

// Query is one recall. Limit and Project are not optional in practice: an
// unbounded recall across the default scope is how a context window gets
// filled with somebody else's project.
type Query struct {
	Text    string
	Project string
	Tags    string
	Limit   int
	Format  string // index (default), full, paths
}

// Recall searches memories. It never writes.
func (c *Client) Recall(q Query) (*RecallResult, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, fmt.Errorf("%w: recall needs a query", ErrInvalidValue)
	}
	if q.Limit <= 0 {
		q.Limit = 5
	}
	if q.Format == "" {
		q.Format = "index"
	}
	// The query goes in as the value of --query rather than as a positional,
	// so a question that happens to begin with a dash stays a question.
	args := []string{"recall", "--query", q.Text, "--json",
		"--limit", fmt.Sprint(q.Limit), "--format", q.Format, "--no-interactive"}
	var err error
	if args, err = appendNamed(args, "--project", q.Project); err != nil {
		return nil, err
	}
	if args, err = appendNamed(args, "--tags", q.Tags); err != nil {
		return nil, err
	}
	raw, err := c.run(args)
	if err != nil {
		return nil, err
	}
	var out RecallResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("memgraph recall did not return the JSON shape this expects: %w", err)
	}
	return &out, nil
}

// Save writes a memory. Only an explicit command reaches this.
func (c *Client) Save(text, project, typ, tags string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: save needs text", ErrInvalidValue)
	}
	args := []string{"save", "--text", text, "--json", "--no-interactive"}
	var err error
	for _, kv := range [][2]string{{"--project", project}, {"--type", typ}, {"--tags", tags}} {
		if args, err = appendNamed(args, kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	return c.run(args)
}

// Read returns one memory, or one section of it (id/slug).
func (c *Client) Read(ref, project string) ([]byte, error) {
	if !nameRe.MatchString(ref) {
		return nil, fmt.Errorf("%w: memory ref %q is not an id or id/slug", ErrInvalidValue, ref)
	}
	args := []string{"read", ref, "--json", "--no-interactive"}
	var err error
	if args, err = appendNamed(args, "--project", project); err != nil {
		return nil, err
	}
	return c.run(args)
}

// Projects lists the scopes memgraph knows about.
func (c *Client) Projects() ([]byte, error) {
	return c.run([]string{"projects", "--json", "--no-interactive"})
}

// appendNamed adds a --flag value pair after validating the value.
func appendNamed(args []string, flag, value string) ([]string, error) {
	if value == "" {
		return args, nil
	}
	if !nameRe.MatchString(value) {
		return nil, fmt.Errorf("%w: %s %q expected letters, digits, . _ - / and no leading dash",
			ErrInvalidValue, flag, value)
	}
	return append(args, flag, value), nil
}

// run executes one memgraph call under a timeout, reading a bounded amount of
// stdout. A non-zero exit carries memgraph's own stderr, which already
// follows the same output spec this tool does.
func (c *Client) run(args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Bin, args...)
	// No inherited stdin: a subprocess that can read the terminal is a
	// subprocess that can hang a non-interactive command forever.
	cmd.Stdin = nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxOutput))
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("memgraph timed out after %s", c.Timeout)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return nil, fmt.Errorf("memgraph %s failed: %s", args[0], msg)
	}
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}
