package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// streamIdleTimeout fails a hung SSE body if no bytes arrive for this long.
// Overall stream length is unbounded; only silence is fatal (gateway wedged).
const streamIdleTimeout = 5 * time.Minute

// idleReader wraps an io.Reader and fails if a single Read blocks longer than idle
// or if the parent ctx is cancelled. Used for SSE so Timeout:0 clients cannot hang forever.
type idleReader struct {
	r    io.Reader
	idle time.Duration
	ctx  context.Context
}

func (i *idleReader) Read(p []byte) (int, error) {
	if i == nil || i.r == nil {
		return 0, io.EOF
	}
	if i.ctx != nil {
		if err := i.ctx.Err(); err != nil {
			return 0, err
		}
	}
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := i.r.Read(p)
		ch <- result{n, err}
	}()
	idle := i.idle
	if idle <= 0 {
		idle = streamIdleTimeout
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	select {
	case <-i.ctx.Done():
		return 0, i.ctx.Err()
	case <-timer.C:
		return 0, fmt.Errorf("llm: stream idle timeout after %s (no data from upstream)", idle)
	case res := <-ch:
		return res.n, res.err
	}
}

// DeltaFn is called with content token deltas during streaming (may be empty for tool-only chunks).
type DeltaFn func(delta string)

// StreamHooks are optional SSE callbacks. Content is the answer; reasoning is
// provider thinking (DeepSeek reasoning, OpenAI reasoning_content, …) and is
// UI-only — never mixed into Message.Content / agent history.
type StreamHooks struct {
	OnContent   DeltaFn
	OnReasoning DeltaFn
}

// streamReq is ChatRequest plus stream flag.
type streamReq struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []ToolSpec      `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	// StreamOptions asks for a final usage chunk (OpenAI spec since 2024;
	// compatible gateways ignore unknown request fields).
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatStreamHooks is like Chat but uses SSE, with separate content and
// reasoning callbacks. Tool calls are assembled from streamed chunks.
func (c *Client) ChatStreamHooks(ctx context.Context, messages []Message, tools []ToolSpec, hooks StreamHooks) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	// ChatStreamHooks is OpenAI chat-completions SSE only. Anthropic is routed in ChatWithStream.
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	url := base + "/chat/completions"
	if strings.HasSuffix(base, "/chat/completions") {
		url = base
	}

	body := streamReq{
		Model: c.Model, Messages: toOpenAIMessages(messages), Tools: tools, Stream: true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	req, err := newJSONRequest(ctx, http.MethodPost, url, raw)
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	res, err := c.doHTTPStream(req)
	if err != nil {
		return Message{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		return Message{}, fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(b), 300))
	}

	msg := Message{Role: "assistant"}
	// tool call id -> accumulating function
	type acc struct {
		id, name, args string
	}
	toolsAcc := map[int]*acc{}

	// Stream HTTP client has Timeout:0 so long generations work; without an idle
	// bound a silent upstream hangs forever (UI stuck on the last → tool line).
	streamBody := &idleReader{r: res.Body, idle: streamIdleTimeout, ctx: ctx}
	sc := bufio.NewScanner(streamBody)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`         // DeepSeek / ZenMux
					ReasoningContent string `json:"reasoning_content"` // some OpenAI-compat
					Thinking         string `json:"thinking"`          // some gateways
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Message{}, fmt.Errorf("llm: %s", chunk.Error.Message)
		}
		// The usage chunk arrives with empty choices — read it before the guard.
		if chunk.Usage != nil {
			msg.Usage = Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if fr := chunk.Choices[0].FinishReason; fr != "" {
			msg.StopReason = fr
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			msg.Content += d.Content
			if hooks.OnContent != nil {
				hooks.OnContent(d.Content)
			}
		}
		// Reasoning is UI-only — never part of Message.Content / tool loop history.
		reason := d.Reasoning
		if reason == "" {
			reason = d.ReasoningContent
		}
		if reason == "" {
			reason = d.Thinking
		}
		if reason != "" && hooks.OnReasoning != nil {
			hooks.OnReasoning(reason)
		}
		for _, tc := range d.ToolCalls {
			a := toolsAcc[tc.Index]
			if a == nil {
				a = &acc{}
				toolsAcc[tc.Index] = a
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name = tc.Function.Name
			}
			a.args += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, err
	}
	// Order tool calls by index. Some gateways send non-contiguous indices (or
	// start above 0), so iterate the actual keys in order — never 0..len-1.
	idxs := make([]int, 0, len(toolsAcc))
	for i := range toolsAcc {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		a := toolsAcc[i]
		if a == nil {
			continue
		}
		args := a.args
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   a.id,
			Type: "function",
			Function: FunctionCall{
				Name:      a.name,
				Arguments: args,
			},
		})
	}
	return msg, nil
}
