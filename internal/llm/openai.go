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
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
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

// Client is a multi-wire chat client (openai-chat-completions or anthropic-messages).
type Client struct {
	// Wire is the client protocol id (see WireOpenAIChat, WireAnthropicMsg).
	Wire         string
	BaseURL      string
	APIKey       string
	Model        string
	HTTP         *http.Client
	ExtraHeaders map[string]string
	// Stream enables SSE token deltas when supported by the wire.
	Stream bool
}

// ChatRequest is the outbound chat body (subset).
type ChatRequest struct {
	Model    string     `json:"model"`
	Messages []Message  `json:"messages"`
	Tools    []ToolSpec `json:"tools,omitempty"`
}

// ChatResponse is the inbound chat body (subset).
type ChatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
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

	body := ChatRequest{Model: c.Model, Messages: messages, Tools: tools}
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
	return msg, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
