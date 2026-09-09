// Package provider is the model layer.
//
// One interface, one implementation today (OpenAI-compatible). The interface
// exists now rather than later because the second provider always arrives
// after the first one's assumptions have leaked into calling code -- tool-call
// shapes and streaming semantics diverge between vendors, and retrofitting the
// seam is where the bugs live.
package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Role values used in a conversation.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Tool roles. A tool result is a turn in the conversation like any other,
// which is why it is stored and replayed like any other.
const RoleTool = "tool"

// Message is one turn.
//
// ToolCalls belong to an assistant turn that asked for work; ToolCallID and
// Name belong to the tool turn that answered one. Both must survive a round
// trip through storage: a provider rejects a tool result whose matching call
// is not in the same conversation, so a thread that drops either half cannot
// be resumed at all.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Tool is a function the model may ask to have run. Parameters is a JSON
// Schema object.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is one such request. Arguments is raw JSON as the model produced
// it -- unparsed on purpose, because a model that emits invalid JSON should
// be told so rather than have this layer guess what it meant.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Request is a completion ask. Temperature is a pointer because "unset" and
// "zero" are different instructions to a model.
type Request struct {
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   int
	Tools       []Tool
}

// Usage is the token accounting a provider reports back.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is a completed turn.
type Response struct {
	Content      string     `json:"content"`
	Model        string     `json:"model"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Usage        Usage      `json:"usage"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
}

// Provider is what the chat engine depends on. onDelta, when non-nil, is
// called with each streamed text chunk; a provider that cannot stream MAY
// ignore it and return the whole answer, so callers must not treat deltas as
// the only source of content -- Response.Content is always complete.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req Request, onDelta func(string)) (*Response, error)
}

// ContextError is a refusal because the prompt did not fit.
//
// It is its own type because it is the one provider failure with a mechanical
// fix: everything else is retried, reported or abandoned, while this one is
// answered by making the prompt smaller. Limit and Used are filled in when
// the provider says them, which most do, and that is how a model's real
// window gets learned rather than guessed.
type ContextError struct {
	Limit   int
	Used    int
	Message string
}

func (e *ContextError) Error() string {
	if e.Limit > 0 && e.Used > 0 {
		return fmt.Sprintf("the prompt does not fit: %d tokens against a %d-token window", e.Used, e.Limit)
	}
	return "the prompt exceeds this model's context window: " + e.Message
}

// IsContextError reports whether err is a context-window refusal.
func IsContextError(err error) (*ContextError, bool) {
	ce, ok := err.(*ContextError)
	return ce, ok
}

// contextMarkers are the phrasings providers use for this refusal. They differ
// per vendor and none of them is a stable code, so the match is deliberately
// broad: a missed match costs a retry that fails, while a false match costs a
// pointless compaction, and both are better than treating it as a hard error.
var contextMarkers = []string{
	"context_length_exceeded", "maximum context length", "context length",
	"context window", "too many tokens", "reduce the length of the messages",
	"prompt is too long", "input is too long",
}

// numbersRe pulls the two token counts out of a message like "This model's
// maximum context length is 8192 tokens. However, your messages resulted in
// 9001 tokens".
var numbersRe = regexp.MustCompile(`(\d[\d_,]{2,})`)

// classifyContext turns a provider's refusal into a ContextError when it looks
// like one, and returns nil when it does not.
func classifyContext(status int, body string) *ContextError {
	low := strings.ToLower(body)
	hit := false
	for _, m := range contextMarkers {
		if strings.Contains(low, m) {
			hit = true
			break
		}
	}
	if !hit {
		return nil
	}
	ce := &ContextError{Message: strings.TrimSpace(body)}
	nums := numbersRe.FindAllString(body, -1)
	clean := func(s string) int {
		s = strings.NewReplacer(",", "", "_", "").Replace(s)
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return n
	}
	// The first plausible number is the window and the second is what was
	// sent -- the order every provider observed here uses. When only one is
	// present it is the window, which is the number worth remembering.
	if len(nums) > 0 {
		ce.Limit = clean(nums[0])
	}
	if len(nums) > 1 {
		ce.Used = clean(nums[1])
	}
	if ce.Limit > 0 && ce.Used > 0 && ce.Used < ce.Limit {
		// Reported the other way round by some providers; the larger number
		// is the one that did not fit.
		ce.Limit, ce.Used = ce.Used, ce.Limit
	}
	return ce
}

// Config is what it takes to reach a provider.
type Config struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

// Factory builds a provider from config.
type Factory func(Config) (Provider, error)

var registry = map[string]Factory{}

// Register adds a provider implementation under a name.
func Register(name string, f Factory) { registry[name] = f }

// New builds the named provider.
func New(name string, cfg Config) (Provider, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %v)", name, Names())
	}
	return f(cfg)
}

// Names lists the registered providers, sorted so output is stable.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
