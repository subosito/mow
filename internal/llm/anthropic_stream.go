package llm

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
)

type anthToolAcc struct {
	id, name string
	// args accumulates streamed input_json_delta fragments; `args += delta`
	// per SSE event is O(n^2) copying for a large tool call.
	args strings.Builder
}

// chatAnthropicStream is the SSE path for anthropic-messages (any host on that wire).
func (c *Client) chatAnthropicStream(ctx context.Context, messages []Message, tools []ToolSpec, hooks StreamHooks) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	url := anthropicMessagesURL(c.BaseURL)

	system, anthMsgs := toAnthropicMessages(messages)
	if c.PromptCache {
		cacheLastMessage(anthMsgs, c.CacheTTL)
	}
	body := map[string]any{
		"model":      c.requestModel(),
		"max_tokens": c.anthropicMaxTokens(),
		"messages":   anthMsgs,
		"stream":     true,
	}
	if sys := anthropicSystemField(c.activeSystemPrefix(), system, c.PromptCache, c.CacheTTL); sys != nil {
		body["system"] = sys
	}
	if atools := toAnthropicTools(tools); len(atools) > 0 {
		if c.PromptCache {
			cacheLastTool(atools, c.CacheTTL)
		}
		body["tools"] = atools
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
	c.setAnthropicAuth(req)
	req.Header.Set("Accept", "text/event-stream")
	c.applyHeaders(req)

	res, err := c.doHTTPStream(req)
	if err != nil {
		return Message{}, err
	}
	complete := false
	defer func() { finishStream(ctx, res.Body, complete) }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		return Message{}, fmt.Errorf("llm: anthropic HTTP %d: %s", res.StatusCode, truncate(string(b), 300))
	}

	msg := Message{Role: "assistant"}
	toolsByIdx := map[int]*anthToolAcc{}
	// Text deltas accumulate here and are flushed to msg.Content after the
	// stream: `msg.Content += delta` per event is O(n^2) copying.
	var content strings.Builder
	started := time.Now()

	// Stream HTTP client has Timeout:0 so long generations work; without an
	// idle bound a silent upstream hangs forever.
	streamBody := &idleReader{r: res.Body, idle: streamIdleTimeout, ctx: ctx}
	defer streamBody.abandon()
	sc := bufio.NewScanner(streamBody)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var eventName string
	activity := onceActivity(hooks.OnActivity)
	// terminal: message_stop (or a message_delta carrying stop_reason)
	// arrived. A clean EOF before that is a dropped connection.
	terminal := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			eventName = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(after)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		// ping keepalives arrive while the model is still silent — counting
		// one would end the wait state on a lie. message_start and everything
		// after it are real upstream work.
		switch resolveSSEType(eventName, data) {
		case "", "ping":
		case "message_stop":
			terminal = true
			fallthrough
		default:
			if activity != nil {
				activity()
			}
		}
		if err := applyAnthropicSSE(data, eventName, &msg, &content, toolsByIdx, hooks); err != nil {
			return Message{}, err
		}
		// Stop parsing at message_stop. Cleanup allows a bounded HTTP tail,
		// never an indefinite wait for a gateway that keeps the stream open.
		if terminal {
			complete = true
			break
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, err
	}
	if !terminal && msg.StopReason == "" {
		return Message{}, &streamIncompleteError{Wire: "anthropic-messages", ContentBytes: content.Len(),
			ToolCalls: len(toolsByIdx), Elapsed: time.Since(started)}
	}
	msg.Content = content.String()
	// Keys are Anthropic content-block indices shared with text blocks, so they
	// are not contiguous from 0 — iterate the actual keys in order.
	idxs := slices.Sorted(maps.Keys(toolsByIdx))
	for _, i := range idxs {
		a := toolsByIdx[i]
		if a == nil {
			continue
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   a.id,
			Type: "function",
			Function: FunctionCall{
				Name:      a.name,
				Arguments: a.args.String(),
			},
		})
	}
	return msg, nil
}

func applyAnthropicSSE(data, event string, msg *Message, content *strings.Builder, toolsByIdx map[int]*anthToolAcc, hooks StreamHooks) error {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(data), &base); err != nil {
		return nil
	}
	typ := base.Type
	if typ == "" {
		typ = event
	}
	switch typ {
	case "error":
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(data), &errBody)
		if errBody.Error.Message != "" {
			return fmt.Errorf("llm: %s", errBody.Error.Message)
		}
		return fmt.Errorf("llm: anthropic stream error")
	case "content_block_start":
		var ev struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if ev.ContentBlock.Type == "tool_use" {
			toolsByIdx[ev.Index] = &anthToolAcc{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
		}
		if ev.ContentBlock.Type == "server_tool_use" || ev.ContentBlock.Type == "web_search_tool_result" {
			// Anthropic ran this itself (native_tools). Record it for
			// reporting only — never as a tool_use, since the agent has no
			// such tool and the loop would fail trying to execute it.
			msg.ProviderCalls = append(msg.ProviderCalls, ProviderCall{
				Type: ev.ContentBlock.Type,
				ID:   ev.ContentBlock.ID,
			})
		}
	case "content_block_delta":
		var ev struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				content.WriteString(ev.Delta.Text)
				if hooks.OnContent != nil {
					hooks.OnContent(ev.Delta.Text)
				}
			}
		case "thinking_delta":
			// Extended thinking — UI-only, not Message.Content.
			t := ev.Delta.Thinking
			if t == "" {
				t = ev.Delta.Text
			}
			if t != "" && hooks.OnReasoning != nil {
				hooks.OnReasoning(t)
			}
		case "input_json_delta":
			if a := toolsByIdx[ev.Index]; a != nil {
				a.args.WriteString(ev.Delta.PartialJSON)
			}
		}
	case "message_start":
		// Carries input token usage (and any initial output count).
		var ev struct {
			Message struct {
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		// Sum cached + non-cached input so the reported total is honest.
		if in := ev.Message.Usage.InputTokens + ev.Message.Usage.CacheCreationInputTokens + ev.Message.Usage.CacheReadInputTokens; in > 0 {
			msg.Usage.InputTokens = in
		}
		// Reads are the discounted share; writes carry a premium. Both are
		// subsets of InputTokens and are priced separately downstream.
		msg.Usage.CachedInputTokens = ev.Message.Usage.CacheReadInputTokens
		msg.Usage.CacheWriteInputTokens = ev.Message.Usage.CacheCreationInputTokens
		if ev.Message.Usage.OutputTokens > 0 {
			msg.Usage.OutputTokens = ev.Message.Usage.OutputTokens
		}
	case "message_delta":
		var ev struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"` // cumulative
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if ev.Delta.StopReason != "" {
			msg.StopReason = ev.Delta.StopReason
		}
		if ev.Usage.OutputTokens > 0 {
			msg.Usage.OutputTokens = ev.Usage.OutputTokens
		}
		// The streamed message_delta carries no server_tool_use counter, so
		// report what the blocks showed. Without this a streamed run says zero
		// provider calls next to an answer built from search results.
		if msg.Usage.ServerSideToolCalls == 0 && len(msg.ProviderCalls) > 0 {
			msg.Usage.ServerSideToolCalls = len(msg.ProviderCalls)
		}
	}
	return nil
}

// setAnthropicAuth sets the credential headers for the anthropic-messages
// wire: x-api-key + anthropic-version for native Anthropic hosts, plus a
// Bearer token for gateways that authenticate every wire the OpenAI way.
func (c *Client) setAnthropicAuth(req *http.Request) {
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
}

func anthropicMessagesURL(baseURL string) string {
	base := cmp.Or(strings.TrimRight(baseURL, "/"), "https://api.anthropic.com")
	if strings.HasSuffix(base, "/v1/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}
