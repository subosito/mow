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
)

type anthToolAcc struct {
	id, name, args string
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
	body := map[string]any{
		"model":      c.Model,
		"max_tokens": c.anthropicMaxTokens(),
		"messages":   anthMsgs,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if len(tools) > 0 {
		body["tools"] = toAnthropicTools(tools)
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
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
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
		return Message{}, fmt.Errorf("llm: anthropic HTTP %d: %s", res.StatusCode, truncate(string(b), 300))
	}

	msg := Message{Role: "assistant"}
	toolsByIdx := map[int]*anthToolAcc{}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var eventName string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			eventName = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if err := applyAnthropicSSE(data, eventName, &msg, toolsByIdx, hooks); err != nil {
			return Message{}, err
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, err
	}
	// Keys are Anthropic content-block indices shared with text blocks, so they
	// are not contiguous from 0 — iterate the actual keys in order.
	idxs := make([]int, 0, len(toolsByIdx))
	for i := range toolsByIdx {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
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
				Arguments: a.args,
			},
		})
	}
	return msg, nil
}

func applyAnthropicSSE(data, event string, msg *Message, toolsByIdx map[int]*anthToolAcc, hooks StreamHooks) error {
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
				msg.Content += ev.Delta.Text
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
				a.args += ev.Delta.PartialJSON
			}
		}
	case "message_start":
		// Carries input token usage (and any initial output count).
		var ev struct {
			Message struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if ev.Message.Usage.InputTokens > 0 {
			msg.Usage.InputTokens = ev.Message.Usage.InputTokens
		}
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
	}
	return nil
}

func anthropicMessagesURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	if strings.HasSuffix(base, "/v1/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}
