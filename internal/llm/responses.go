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

// responsesReq is the POST /v1/responses body (subset we speak).
type responsesReq struct {
	Model        string           `json:"model"`
	Input        []map[string]any `json:"input"`
	Instructions string           `json:"instructions,omitempty"`
	Tools        []map[string]any `json:"tools,omitempty"`
	Stream       bool             `json:"stream,omitempty"`
	// store:false keeps the harness stateless (sessions own history).
	Store           *bool `json:"store,omitempty"`
	MaxOutputTokens int   `json:"max_output_tokens,omitempty"`
}

// responsesAPIResponse is the non-stream Responses object we care about.
type responsesAPIResponse struct {
	Status string `json:"status"`
	Output []struct {
		Type      string `json:"type"`
		Role      string `json:"role,omitempty"`
		Status    string `json:"status,omitempty"`
		CallID    string `json:"call_id,omitempty"`
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
		// function_call may also use id as item id; call_id is what we replay.
		ID      string `json:"id,omitempty"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content,omitempty"`
	} `json:"output"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
}

func responsesURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	return base + "/responses"
}

// toResponsesInput maps mow history to Responses input items + instructions.
// System turns become top-level instructions; assistant tool_calls become
// function_call items; tool results become function_call_output.
func toResponsesInput(messages []Message) (instructions string, input []map[string]any) {
	for _, m := range messages {
		switch m.Role {
		case "system":
			if instructions != "" {
				instructions += "\n\n"
			}
			instructions += m.Content
		case "user":
			input = append(input, map[string]any{
				"role":    "user",
				"content": m.Content,
			})
		case "assistant":
			if m.Content != "" {
				input = append(input, map[string]any{
					"role":    "assistant",
					"content": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Function.Arguments
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				callID := tc.ID
				if callID == "" {
					callID = "call_" + tc.Function.Name
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      tc.Function.Name,
					"arguments": args,
				})
			}
			// Pure-empty assistant (no content, no tools) is a no-op.
		case "tool":
			callID := m.ToolCallID
			if callID == "" {
				callID = "call_unknown"
			}
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  m.Content, // may be empty string
			})
		default:
			// Unknown roles: send as user text so history is not dropped.
			if m.Content != "" {
				input = append(input, map[string]any{
					"role":    "user",
					"content": m.Content,
				})
			}
		}
	}
	return instructions, input
}

// toResponsesTools flattens OpenAI chat ToolSpec into Responses function tools.
// strict:false matches non-strict agent schemas (chat-completions default).
func toResponsesTools(tools []ToolSpec) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var schema any
		if len(t.Function.Parameters) > 0 {
			_ = json.Unmarshal(t.Function.Parameters, &schema)
		}
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":   schema,
			"strict":      false,
		})
	}
	return out
}

func (c *Client) buildResponsesBody(messages []Message, tools []ToolSpec, stream bool) responsesReq {
	instructions, input := toResponsesInput(messages)
	// Empty input is invalid; providers need at least one user turn.
	if len(input) == 0 {
		input = []map[string]any{{"role": "user", "content": ""}}
	}
	storeFalse := false
	body := responsesReq{
		Model:        c.Model,
		Input:        input,
		Instructions: instructions,
		Tools:        toResponsesTools(tools),
		Stream:       stream,
		Store:        &storeFalse,
	}
	if c.MaxTokens > 0 {
		body.MaxOutputTokens = c.MaxTokens
	}
	return body
}

func messageFromResponses(parsed responsesAPIResponse) Message {
	msg := Message{Role: "assistant"}
	var textParts []string
	for _, item := range parsed.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" || part.Type == "text" {
					textParts = append(textParts, part.Text)
				}
			}
		case "function_call":
			args := item.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   id,
				Type: "function",
				Function: FunctionCall{
					Name:      item.Name,
					Arguments: args,
				},
			})
		case "reasoning":
			// UI-only in stream path; non-stream we ignore (no history leak).
		}
	}
	msg.Content = strings.Join(textParts, "")
	if parsed.Usage != nil {
		msg.Usage = Usage{
			InputTokens:  parsed.Usage.InputTokens,
			OutputTokens: parsed.Usage.OutputTokens,
		}
	}
	// Map status to a chat-like stop reason for Truncated() and logs.
	switch {
	case parsed.IncompleteDetails != nil && parsed.IncompleteDetails.Reason != "":
		msg.StopReason = parsed.IncompleteDetails.Reason
	case parsed.Status == "incomplete":
		msg.StopReason = "max_tokens"
	case len(msg.ToolCalls) > 0:
		msg.StopReason = "tool_calls"
	case parsed.Status != "":
		msg.StopReason = parsed.Status // "completed"
	default:
		msg.StopReason = "stop"
	}
	return msg
}

// chatOpenAIResponses is the non-stream path for WireOpenAIResponses.
func (c *Client) chatOpenAIResponses(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	url := responsesURL(c.BaseURL)
	body := c.buildResponsesBody(messages, tools, false)
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
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	res, err := c.doHTTP(req)
	if err != nil {
		return Message{}, err
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return Message{}, err
	}
	var parsed responsesAPIResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Message{}, fmt.Errorf("llm: responses decode: %w (status %d body %s)", err, res.StatusCode, truncate(string(respBody), 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Message{}, fmt.Errorf("llm: %s", parsed.Error.Message)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Message{}, fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(respBody), 300))
	}
	return messageFromResponses(parsed), nil
}

type responsesToolAcc struct {
	callID, name, args string
}

// chatOpenAIResponsesStream is the SSE path for WireOpenAIResponses.
func (c *Client) chatOpenAIResponsesStream(ctx context.Context, messages []Message, tools []ToolSpec, hooks StreamHooks) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	url := responsesURL(c.BaseURL)
	body := c.buildResponsesBody(messages, tools, true)
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
	toolsByIdx := map[int]*responsesToolAcc{}
	// item_id → output_index for argument deltas that only carry item_id.
	itemToIdx := map[string]int{}

	streamBody := &idleReader{r: res.Body, idle: streamIdleTimeout, ctx: ctx}
	sc := bufio.NewScanner(streamBody)
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
		if err := applyResponsesSSE(data, eventName, &msg, toolsByIdx, itemToIdx, hooks); err != nil {
			return Message{}, err
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, err
	}

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
		args := a.args
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   a.callID,
			Type: "function",
			Function: FunctionCall{
				Name:      a.name,
				Arguments: args,
			},
		})
	}
	if msg.StopReason == "" {
		if len(msg.ToolCalls) > 0 {
			msg.StopReason = "tool_calls"
		} else {
			msg.StopReason = "stop"
		}
	}
	return msg, nil
}

func applyResponsesSSE(data, event string, msg *Message, toolsByIdx map[int]*responsesToolAcc, itemToIdx map[string]int, hooks StreamHooks) error {
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
			Message string `json:"message"`
		}
		_ = json.Unmarshal([]byte(data), &errBody)
		msgText := errBody.Error.Message
		if msgText == "" {
			msgText = errBody.Message
		}
		if msgText != "" {
			return fmt.Errorf("llm: %s", msgText)
		}
		return fmt.Errorf("llm: responses stream error")

	case "response.output_text.delta":
		var ev struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if ev.Delta != "" {
			msg.Content += ev.Delta
			if hooks.OnContent != nil {
				hooks.OnContent(ev.Delta)
			}
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		// UI-only reasoning; never into Message.Content.
		var ev struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil || ev.Delta == "" {
			return nil
		}
		if hooks.OnReasoning != nil {
			hooks.OnReasoning(ev.Delta)
		}

	case "response.output_item.added", "response.output_item.done":
		var ev struct {
			OutputIndex int `json:"output_index"`
			Item        struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if ev.Item.Type != "function_call" {
			return nil
		}
		a := toolsByIdx[ev.OutputIndex]
		if a == nil {
			a = &responsesToolAcc{}
			toolsByIdx[ev.OutputIndex] = a
		}
		if ev.Item.ID != "" {
			itemToIdx[ev.Item.ID] = ev.OutputIndex
		}
		if ev.Item.CallID != "" {
			a.callID = ev.Item.CallID
		}
		if a.callID == "" && ev.Item.ID != "" {
			a.callID = ev.Item.ID
		}
		if ev.Item.Name != "" {
			a.name = ev.Item.Name
		}
		// xAI may deliver the whole call in one chunk (no argument deltas).
		if typ == "response.output_item.done" && ev.Item.Arguments != "" && a.args == "" {
			a.args = ev.Item.Arguments
		}

	case "response.function_call_arguments.delta":
		var ev struct {
			Delta       string `json:"delta"`
			OutputIndex int    `json:"output_index"`
			ItemID      string `json:"item_id"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		idx := ev.OutputIndex
		if id, ok := itemToIdx[ev.ItemID]; ok {
			idx = id
		}
		a := toolsByIdx[idx]
		if a == nil {
			a = &responsesToolAcc{}
			toolsByIdx[idx] = a
		}
		a.args += ev.Delta

	case "response.function_call_arguments.done":
		var ev struct {
			Arguments   string `json:"arguments"`
			OutputIndex int    `json:"output_index"`
			ItemID      string `json:"item_id"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		idx := ev.OutputIndex
		if id, ok := itemToIdx[ev.ItemID]; ok {
			idx = id
		}
		a := toolsByIdx[idx]
		if a == nil {
			a = &responsesToolAcc{}
			toolsByIdx[idx] = a
		}
		if ev.Arguments != "" {
			a.args = ev.Arguments
		}

	case "response.completed":
		var ev struct {
			Response responsesAPIResponse `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		// Prefer usage / stop reason from the completed envelope.
		full := messageFromResponses(ev.Response)
		if !full.Usage.Zero() {
			msg.Usage = full.Usage
		}
		if full.StopReason != "" {
			msg.StopReason = full.StopReason
		}
		// If stream deltas were empty but completed carries full text/tools
		// (some gateways batch), fill gaps without duplicating.
		if msg.Content == "" && full.Content != "" {
			msg.Content = full.Content
			if hooks.OnContent != nil {
				hooks.OnContent(full.Content)
			}
		}
		if len(toolsByIdx) == 0 && len(full.ToolCalls) > 0 {
			for i, tc := range full.ToolCalls {
				toolsByIdx[i] = &responsesToolAcc{
					callID: tc.ID,
					name:   tc.Function.Name,
					args:   tc.Function.Arguments,
				}
			}
		}

	case "response.incomplete":
		msg.StopReason = "max_tokens"
	}
	return nil
}
