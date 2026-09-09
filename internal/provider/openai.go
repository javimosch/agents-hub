package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

func init() { Register("openai", func(c Config) (Provider, error) { return newOpenAI(c) }) }

// openAI speaks the /chat/completions shape. That shape is not OpenAI's alone
// any more -- Mistral, Ollama, vLLM, OpenRouter, LM Studio and llama.cpp all
// serve it -- so one implementation plus a base URL covers the whole EU/local
// model story without a second client.
type openAI struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newOpenAI(c Config) (*openAI, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	t := c.Timeout
	if t <= 0 {
		t = 120 * time.Second
	}
	return &openAI{baseURL: base, apiKey: c.APIKey, http: &http.Client{Timeout: t}}, nil
}

func (o *openAI) Name() string { return "openai" }

func (o *openAI) Chat(ctx context.Context, req Request, onDelta func(string)) (*Response, error) {
	stream := onDelta != nil
	body := map[string]any{
		"model":    req.Model,
		"messages": wireMessages(mergeSystem(req.Messages)),
		"stream":   stream,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": t.Name, "description": t.Description, "parameters": t.Parameters,
				},
			})
		}
		body["tools"] = tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if stream {
		// Without this, most servers omit usage entirely on a streamed call
		// and the stored message would carry a silent zero token count.
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable at %s: %w", o.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if stream {
		return o.readStream(resp.Body, onDelta)
	}
	return o.readWhole(resp.Body)
}

// wireMessages maps this package's message onto the shape the API expects.
//
// The differences are small and all load-bearing: a tool result carries
// tool_call_id and no tool_calls, an assistant turn that asked for work
// carries tool_calls and often an empty content, and content must still be
// present as a key even when empty or some servers reject the message.
func wireMessages(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		w := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == RoleTool {
			w["tool_call_id"] = m.ToolCallID
			if m.Name != "" {
				w["name"] = m.Name
			}
			out = append(out, w)
			continue
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				calls = append(calls, map[string]any{
					"id": c.ID, "type": "function",
					"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
				})
			}
			w["tool_calls"] = calls
		}
		out = append(out, w)
	}
	return out
}

// mergeSystem collapses the leading run of system messages into one.
//
// Several servers behind this wire shape keep only the first system message
// and silently drop the rest -- DeepSeek via OpenRouter does, which is how a
// recalled-memory block can be built, reported, sent, and still never reach
// the model. Nothing errors; the prompt token count is simply too small to
// contain what was sent. Merging costs nothing and removes the whole class.
func mergeSystem(msgs []Message) []Message {
	n := 0
	for n < len(msgs) && msgs[n].Role == RoleSystem {
		n++
	}
	if n < 2 {
		return msgs
	}
	parts := make([]string, 0, n)
	for _, m := range msgs[:n] {
		if m.Content != "" {
			parts = append(parts, m.Content)
		}
	}
	out := make([]Message, 0, len(msgs)-n+1)
	out = append(out, Message{Role: RoleSystem, Content: strings.Join(parts, "\n\n")})
	return append(out, msgs[n:]...)
}

// oaToolCall is the wire form of a tool call. In a stream, Index identifies
// which call a fragment belongs to and Arguments arrives a few characters at
// a time, so fragments must be accumulated per index rather than per message.
type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaChoice struct {
	Message struct {
		Content   string       `json:"content"`
		ToolCalls []oaToolCall `json:"tool_calls"`
	} `json:"message"`
	Delta struct {
		Content   string       `json:"content"`
		ToolCalls []oaToolCall `json:"tool_calls"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

func toolCallsFrom(in []oaToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(in))
	for _, c := range in {
		out = append(out, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

type oaResponse struct {
	Model   string     `json:"model"`
	Choices []oaChoice `json:"choices"`
	Usage   *Usage     `json:"usage"`
}

func (o *openAI) readWhole(r io.Reader) (*Response, error) {
	var body oaResponse
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, fmt.Errorf("provider sent an unreadable body: %w", err)
	}
	if len(body.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices")
	}
	out := &Response{
		Content:      body.Choices[0].Message.Content,
		Model:        body.Model,
		FinishReason: body.Choices[0].FinishReason,
		ToolCalls:    toolCallsFrom(body.Choices[0].Message.ToolCalls),
	}
	if body.Usage != nil {
		out.Usage = *body.Usage
	}
	return out, nil
}

// readStream consumes SSE. It accumulates the full text as well as emitting
// deltas, so a caller that only wants the final answer can pass a no-op and
// still get everything.
func (o *openAI) readStream(r io.Reader, onDelta func(string)) (*Response, error) {
	var sb strings.Builder
	out := &Response{}
	// Accumulated by index, then flattened in index order at the end: the
	// fragments of one call's arguments arrive across many events, and the
	// events for two calls are interleaved.
	calls := map[int]*ToolCall{}
	var order []int
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk oaResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // a keep-alive or a shape we don't model; not fatal
		}
		if chunk.Model != "" {
			out.Model = chunk.Model
		}
		if chunk.Usage != nil {
			out.Usage = *chunk.Usage
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				sb.WriteString(c.Delta.Content)
				onDelta(c.Delta.Content)
			}
			for _, tc := range c.Delta.ToolCalls {
				acc, seen := calls[tc.Index]
				if !seen {
					acc = &ToolCall{}
					calls[tc.Index] = acc
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				acc.Arguments += tc.Function.Arguments
			}
			if c.FinishReason != "" {
				out.FinishReason = c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stream broke: %w", err)
	}
	out.Content = sb.String()
	sort.Ints(order)
	for _, i := range order {
		out.ToolCalls = append(out.ToolCalls, *calls[i])
	}
	return out, nil
}
