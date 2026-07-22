// Package llm talks to OpenAI-compatible (and optionally Anthropic) chat APIs.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is a chat message in OpenAI-ish shape.
//
// Content uses omitempty for session/history JSON. OpenAI chat/completions
// requests go through toOpenAIMessages, which always emits content as a string
// (including "") so gateways with a strict MessageContent enum accept
// tool-call turns and empty tool results.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// StopReason is why the provider stopped generating (OpenAI finish_reason /
	// Anthropic stop_reason). "max_tokens" or "length" means the reply was
	// truncated at the token limit. Response-only; never sent on the wire.
	StopReason string `json:"-"`
	// Usage is provider-reported token counts for the call that produced this
	// message (zero when the provider sent none). Response-only.
	Usage Usage `json:"-"`
}

// openAIMessage is the chat/completions wire shape. Content is always a JSON
// string (never omitted, never null). Many OpenAI-compatible gateways type
// content as an untagged enum Text(String)|Parts([...]) with no null variant;
// assistant tool-call turns and empty tool results then 400 with
// "data did not match any variant of untagged enum MessageContent" if content
// is missing or null. Emitting "" is accepted by OpenAI and those gateways.
type openAIMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// toOpenAIMessages maps internal history to the OpenAI wire shape.
func toOpenAIMessages(in []Message) []openAIMessage {
	out := make([]openAIMessage, len(in))
	for i, m := range in {
		out[i] = openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if len(m.ToolCalls) == 0 {
			continue
		}
		tcs := make([]ToolCall, len(m.ToolCalls))
		for j, tc := range m.ToolCalls {
			tcs[j] = tc
			if tcs[j].Type == "" {
				tcs[j].Type = "function"
			}
			// Empty arguments are invalid JSON for most tool schemas; models
			// sometimes stream a name with no arg chunks.
			if strings.TrimSpace(tcs[j].Function.Arguments) == "" {
				tcs[j].Function.Arguments = "{}"
			}
		}
		out[i].ToolCalls = tcs
	}
	return out
}

// Usage counts provider-reported tokens for one chat call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Zero reports whether no counts were recorded (provider sent no usage).
func (u Usage) Zero() bool { return u.InputTokens == 0 && u.OutputTokens == 0 }

// Add returns the element-wise sum (accumulating usage across loop turns).
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens + o.InputTokens,
		OutputTokens: u.OutputTokens + o.OutputTokens,
	}
}

// Truncated reports whether the provider cut the reply at its token limit.
func (m Message) Truncated() bool {
	return m.StopReason == "max_tokens" || m.StopReason == "length"
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds name + JSON arguments string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSpec is exposed to the model as a function tool.
type ToolSpec struct {
	Type     string           `json:"type"`
	Function ToolSpecFunction `json:"function"`
}

// ToolSpecFunction is the OpenAI tools[].function object.
type ToolSpecFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Client is a multi-wire chat client (chat-completions, responses, or anthropic-messages).
type Client struct {
	// Wire is the client protocol id (see WireOpenAIChat, WireOpenAIResponses, WireAnthropicMsg).
	Wire         string
	BaseURL      string
	APIKey       string
	Model        string
	HTTP         *http.Client
	ExtraHeaders map[string]string
	// Stream enables SSE token deltas when supported by the wire.
	Stream bool
	// MaxTokens caps the response length on wires that require it
	// (anthropic-messages, openai-responses max_output_tokens). Zero means
	// provider default (8192 for Anthropic; omit for Responses).
	MaxTokens int
}

// ChatRequest is the outbound chat body (subset).
type ChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []ToolSpec      `json:"tools,omitempty"`
}

// ChatResponse is the inbound chat body (subset).
type ChatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Chat sends messages and optional tools; returns the assistant message.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	return c.ChatWithDelta(ctx, messages, tools, nil)
}

// ChatWithDelta is Chat with optional content delta callback (SSE when stream/onDelta).
func (c *Client) ChatWithDelta(ctx context.Context, messages []Message, tools []ToolSpec, onDelta DeltaFn) (Message, error) {
	return c.ChatWithStream(ctx, messages, tools, StreamHooks{OnContent: onDelta})
}

// ChatWithStream is Chat with content and reasoning SSE callbacks.
func (c *Client) ChatWithStream(ctx context.Context, messages []Message, tools []ToolSpec, hooks StreamHooks) (Message, error) {
	stream := c.Stream || hooks.OnContent != nil || hooks.OnReasoning != nil
	switch NormalizeWire(c.Wire) {
	case WireAnthropicMsg:
		if stream {
			return c.chatAnthropicStream(ctx, messages, tools, hooks)
		}
		return c.chatAnthropic(ctx, messages, tools)
	case WireOpenAIResponses:
		if stream {
			return c.chatOpenAIResponsesStream(ctx, messages, tools, hooks)
		}
		return c.chatOpenAIResponses(ctx, messages, tools)
	default: // WireOpenAIChat
		if stream {
			return c.ChatStreamHooks(ctx, messages, tools, hooks)
		}
		return c.chatOpenAI(ctx, messages, tools)
	}
}

func (c *Client) chatOpenAI(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	url := base + "/chat/completions"
	if strings.HasSuffix(base, "/chat/completions") {
		url = base
	}

	body := ChatRequest{Model: c.Model, Messages: toOpenAIMessages(messages), Tools: tools}
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
	var parsed ChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Message{}, fmt.Errorf("llm: decode: %w (status %d body %s)", err, res.StatusCode, truncate(string(respBody), 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Message{}, fmt.Errorf("llm: %s", parsed.Error.Message)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Message{}, fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(respBody), 300))
	}
	if len(parsed.Choices) == 0 {
		return Message{}, fmt.Errorf("llm: empty choices")
	}
	msg := parsed.Choices[0].Message
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	msg.StopReason = parsed.Choices[0].FinishReason
	msg.Usage = Usage{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	return msg, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
