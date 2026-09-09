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
	"sort"
	"time"
)

// Role values used in a conversation.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a completion ask. Temperature is a pointer because "unset" and
// "zero" are different instructions to a model.
type Request struct {
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   int
}

// Usage is the token accounting a provider reports back.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is a completed turn.
type Response struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	FinishReason string `json:"finish_reason,omitempty"`
	Usage        Usage  `json:"usage"`
}

// Provider is what the chat engine depends on. onDelta, when non-nil, is
// called with each streamed text chunk; a provider that cannot stream MAY
// ignore it and return the whole answer, so callers must not treat deltas as
// the only source of content -- Response.Content is always complete.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req Request, onDelta func(string)) (*Response, error)
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
