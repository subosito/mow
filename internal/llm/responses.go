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

// responsesContentPart is one content part inside a message output item.
type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// responsesOutputItem is one output item in a Responses object.
type responsesOutputItem struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Status    string `json:"status,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call may also use id as item id; call_id is what we replay.
	ID      string                 `json:"id,omitempty"`
	Content []responsesContentPart `json:"content,omitempty"`
}

// responsesAPIResponse is the non-stream Responses object we care about.
type responsesAPIResponse struct {
	Status string                `json:"status"`
	Output []responsesOutputItem `json:"output"`
	Usage  *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		// Server-side tool accounting (web_search and friends) — present when
		// the model executed declared native tools itself.
		NumSourcesUsed         int `json:"num_sources_used"`
		NumServerSideToolCalls int `json:"num_server_side_tool_calls"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
}

func responsesURL(baseURL string) string {
	base := cmp.Or(strings.TrimRight(baseURL, "/"), "https://api.openai.com/v1")
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
			content := any(m.Content)
			if len(m.Parts) > 0 {
				var parts []map[string]any
				if m.Content != "" {
					parts = append(parts, map[string]any{"type": "input_text", "text": m.Content})
				}
				for _, p := range m.Parts {
					switch p.Type {
					case "text":
						parts = append(parts, map[string]any{"type": "input_text", "text": p.Text})
					case "image":
						parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + p.MimeType + ";base64," + p.Data})
					case "audio":
						parts = append(parts, map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": p.Data, "format": audioFormat(p.MimeType)}})
					}
				}
				content = parts
			}
			input = append(input, map[string]any{
				"role":    "user",
				"content": content,
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

// isProviderToolCallItem reports whether a Responses output item type is a
// provider-executed tool call (web_search_call, code_interpreter_call, …).
// function_call is excluded: that is a model-requested call mow executes.
func isProviderToolCallItem(typ string) bool {
	return typ != "function_call" && strings.HasSuffix(typ, "_call")
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
			"parameters":  schema,
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
		Model:        c.requestModel(),
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
			if strings.TrimSpace(item.Name) == "" {
				continue
			}
			args := item.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			if id == "" {
				continue
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
		default:
			if isProviderToolCallItem(item.Type) {
				// Provider-executed tool call (web_search_call,
				// code_interpreter_call, …): already run server-side. Record
				// for observability only — never put these in msg.ToolCalls,
				// the agent loop would try to execute tools it does not have.
				msg.ProviderCalls = append(msg.ProviderCalls, ProviderCall{
					Type:   item.Type,
					ID:     item.ID,
					Status: item.Status,
				})
			}
		}
	}
	msg.Content = strings.Join(textParts, "")
	if parsed.Usage != nil {
		msg.Usage = Usage{
			InputTokens:         parsed.Usage.InputTokens,
			OutputTokens:        parsed.Usage.OutputTokens,
			SourcesUsed:         parsed.Usage.NumSourcesUsed,
			ServerSideToolCalls: parsed.Usage.NumServerSideToolCalls,
		}
	}
	// Streaming envelopes may omit the server-side counter even when the
	// provider ran tools, so fall back to what we actually observed. Otherwise
	// a streamed run reports zero provider calls while citing search results.
	if msg.Usage.ServerSideToolCalls == 0 && len(msg.ProviderCalls) > 0 {
		msg.Usage.ServerSideToolCalls = len(msg.ProviderCalls)
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
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	status, respBody, err := c.doJSON(req)
	if err != nil {
		return Message{}, err
	}
	var parsed responsesAPIResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Message{}, fmt.Errorf("llm: responses decode: %w (status %d body %s)", err, status, truncate(string(respBody), 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Message{}, fmt.Errorf("llm: %s", parsed.Error.Message)
	}
	if status < 200 || status >= 300 {
		return Message{}, fmt.Errorf("llm: HTTP %d: %s", status, truncate(string(respBody), 300))
	}
	// status "failed" with no error body is still a failure, not an empty answer.
	if parsed.Status == "failed" {
		return Message{}, fmt.Errorf("llm: response failed")
	}
	return messageFromResponses(parsed), nil
}

type responsesToolAcc struct {
	callID, name string
	// args accumulates streamed argument fragments; `args += delta` per SSE
	// event is O(n^2) copying for a large tool call.
	args strings.Builder
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
	complete := false
	defer func() { finishStream(ctx, res.Body, complete) }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		return Message{}, fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(b), 300))
	}

	msg := Message{Role: "assistant"}
	toolsByIdx := map[int]*responsesToolAcc{}
	// item_id → output_index for argument deltas that only carry item_id.
	itemToIdx := map[string]int{}
	// Text deltas accumulate here and are flushed to msg.Content after the
	// stream (see applyResponsesSSE).
	var content strings.Builder

	streamBody := &idleReader{r: res.Body, idle: streamIdleTimeout, ctx: ctx}
	defer streamBody.abandon()
	sc := bufio.NewScanner(streamBody)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var eventName string
	activity := onceActivity(hooks.OnActivity)
	// terminal: a response.completed / response.incomplete envelope arrived.
	// A clean EOF before one is a dropped connection, not an empty answer.
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
		// response.created / response.in_progress only acknowledge the
		// request — they arrive when the stream opens, before the model has
		// produced anything, so they are not activity. Later frames (output
		// items, deltas, completed, error) are real upstream work.
		switch resolveSSEType(eventName, data) {
		case "", "response.created", "response.in_progress":
		case "response.completed", "response.incomplete":
			terminal = true
			fallthrough
		default:
			if activity != nil {
				activity()
			}
		}
		if err := applyResponsesSSE(data, eventName, &msg, &content, toolsByIdx, itemToIdx, hooks); err != nil {
			return Message{}, err
		}
		// Stop parsing at the terminal frame; cleanup briefly drains to HTTP EOF.
		if terminal {
			complete = true
			break
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, err
	}
	if !terminal {
		return Message{}, errStreamIncomplete
	}

	msg.Content = content.String()

	idxs := slices.Sorted(maps.Keys(toolsByIdx))
	for _, i := range idxs {
		a := toolsByIdx[i]
		if a == nil || strings.TrimSpace(a.name) == "" || strings.TrimSpace(a.callID) == "" {
			continue
		}
		args := a.args.String()
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

// applyResponsesSSE folds one SSE event into msg. Text deltas go into content
// (flushed to msg.Content by the caller after the stream) so a long reply does
// not re-copy the whole answer once per delta.
func applyResponsesSSE(data, event string, msg *Message, content *strings.Builder, toolsByIdx map[int]*responsesToolAcc, itemToIdx map[string]int, hooks StreamHooks) error {
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
			if out := appendSnapshotDelta(content, ev.Delta); out != "" && hooks.OnContent != nil {
				hooks.OnContent(out)
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
				Status    string `json:"status"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if isProviderToolCallItem(ev.Item.Type) {
			// Provider-executed call: record on added, refresh status on
			// done. Never a ToolCall — mow does not execute these.
			if ev.Item.ID != "" {
				for i := range msg.ProviderCalls {
					if msg.ProviderCalls[i].ID == ev.Item.ID {
						if typ == "response.output_item.done" && ev.Item.Status != "" {
							msg.ProviderCalls[i].Status = ev.Item.Status
						}
						return nil
					}
				}
			} else if typ == "response.output_item.done" {
				return nil // id-less duplicate of the added event
			}
			msg.ProviderCalls = append(msg.ProviderCalls, ProviderCall{
				Type: ev.Item.Type,
				ID:   ev.Item.ID,
			})
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
		if typ == "response.output_item.done" && ev.Item.Arguments != "" && a.args.Len() == 0 {
			a.args.WriteString(ev.Item.Arguments)
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
			// Argument events must belong to a preceding function_call output
			// item. Creating an accumulator here turns an orphan `{}`/sentinel
			// into a nameless tool call that bricks replay on the next request.
			return nil
		}
		a.args.WriteString(ev.Delta)

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
			return nil // orphan done event; no valid function_call metadata
		}
		if ev.Arguments != "" {
			a.args.Reset()
			a.args.WriteString(ev.Arguments)
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
		if content.Len() == 0 && full.Content != "" {
			content.WriteString(full.Content)
			if hooks.OnContent != nil {
				hooks.OnContent(full.Content)
			}
		}
		if len(toolsByIdx) == 0 && len(full.ToolCalls) > 0 {
			for i, tc := range full.ToolCalls {
				a := &responsesToolAcc{
					callID: tc.ID,
					name:   tc.Function.Name,
				}
				a.args.WriteString(tc.Function.Arguments)
				toolsByIdx[i] = a
			}
		}
		if len(msg.ProviderCalls) == 0 && len(full.ProviderCalls) > 0 {
			msg.ProviderCalls = full.ProviderCalls
		}

	case "response.incomplete":
		msg.StopReason = "max_tokens"

	case "response.failed":
		// {"type":"response.failed","response":{"status":"failed","error":{...}}}
		var ev struct {
			Response struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		_ = json.Unmarshal([]byte(data), &ev)
		if ev.Response.Error != nil && ev.Response.Error.Message != "" {
			return fmt.Errorf("llm: %s", ev.Response.Error.Message)
		}
		return fmt.Errorf("llm: response failed")
	}
	return nil
}
