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
//
// The upstream Read runs on a pump goroutine with its own buffer: after an idle
// timeout or ctx cancel we abandon that read, and it must not keep writing into
// the caller's slice (bufio.Scanner reuses its buffer) — that would be a data
// race and could corrupt an unrelated later read.
type idleReader struct {
	r    io.Reader
	idle time.Duration
	ctx  context.Context

	pump    chan pumpResult // results from the background reader
	want    chan struct{}   // request one read from the pump
	buf     []byte          // pump-owned scratch buffer
	pending []byte          // bytes read by the pump but not yet handed to caller
	err     error           // sticky terminal error from the pump
	done    bool            // pump abandoned (timeout/cancel) — never reuse it
}

type pumpResult struct {
	n   int
	err error
}

func (i *idleReader) idleDur() time.Duration {
	if i.idle <= 0 {
		return streamIdleTimeout
	}
	return i.idle
}

func (i *idleReader) ctxDone() <-chan struct{} {
	if i.ctx == nil {
		return nil // nil channel: never ready
	}
	return i.ctx.Done()
}

func (i *idleReader) ctxErr() error {
	if i.ctx == nil {
		return nil
	}
	return i.ctx.Err()
}

func (i *idleReader) Read(p []byte) (int, error) {
	if i == nil || i.r == nil {
		return 0, io.EOF
	}
	if err := i.ctxErr(); err != nil {
		return 0, err
	}
	if len(i.pending) > 0 {
		n := copy(p, i.pending)
		i.pending = i.pending[n:]
		return n, nil
	}
	if i.err != nil {
		return 0, i.err
	}
	if i.done {
		return 0, fmt.Errorf("llm: stream reader abandoned")
	}
	if len(p) == 0 {
		return 0, nil
	}

	if i.pump == nil {
		i.pump = make(chan pumpResult, 1)
		i.want = make(chan struct{}, 1)
		i.buf = make([]byte, 32*1024)
		go i.run()
	}
	// Ask the pump for exactly one read; it never touches i.buf again until
	// the next request, so the copy below is race-free.
	i.want <- struct{}{}

	timer := time.NewTimer(i.idleDur())
	defer timer.Stop()
	select {
	case <-i.ctxDone():
		i.done = true
		return 0, i.ctxErr()
	case <-timer.C:
		i.done = true
		return 0, fmt.Errorf("llm: stream idle timeout after %s (no data from upstream)", i.idleDur())
	case res := <-i.pump:
		if res.n > 0 {
			n := copy(p, i.buf[:res.n])
			if n < res.n {
				i.pending = append(i.pending[:0], i.buf[n:res.n]...)
			}
			if res.err != nil {
				i.err = res.err
			}
			return n, nil
		}
		if res.err != nil {
			i.err = res.err
			return 0, res.err
		}
		return 0, nil
	}
}

// run pumps one Read per request from Read; it stops as soon as a read fails
// or the caller abandons it (want is never signalled again).
func (i *idleReader) run() {
	for range i.want {
		n, err := i.r.Read(i.buf)
		i.pump <- pumpResult{n, err}
		if err != nil {
			return
		}
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
		Model: c.requestModel(), Messages: toOpenAIMessages(messages), Tools: tools, Stream: true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	raw, err = c.finalizeChatBody(raw)
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
	// tool call index -> accumulating function (+ optional thought_signature)
	type acc struct {
		id, name, args, thoughtSig string
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
					Reasoning        string `json:"reasoning"`         // some OpenAI-compat
					ReasoningContent string `json:"reasoning_content"` // some OpenAI-compat
					Thinking         string `json:"thinking"`          // some gateways
					ToolCalls        []struct {
						Index            int    `json:"index"`
						ID               string `json:"id"`
						Type             string `json:"type"`
						ThoughtSignature string `json:"thought_signature"`
						Function         struct {
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
			// Some providers send thought_signature once on the start chunk; keep first non-empty.
			if a.thoughtSig == "" {
				if s := strings.TrimSpace(tc.ThoughtSignature); s != "" {
					a.thoughtSig = s
				}
			}
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
			ID:               a.id,
			Type:             "function",
			ThoughtSignature: a.thoughtSig,
			Function: FunctionCall{
				Name:      a.name,
				Arguments: args,
			},
		})
	}
	return msg, nil
}
