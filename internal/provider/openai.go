package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		"messages": req.Messages,
		"stream":   stream,
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

type oaChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Delta struct {
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
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
			if c.FinishReason != "" {
				out.FinishReason = c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stream broke: %w", err)
	}
	out.Content = sb.String()
	return out, nil
}
