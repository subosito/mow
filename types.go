package mow

import (
	"context"
	"encoding/json"
)

// Tool is a host-executed function available to the agent loop.
// Extension packs implement this and register via ext.RegisterTool.
// (Defined here so internal packages do not need to import ext.)
type Tool interface {
	Name() string
	Description() string
	// Parameters is a JSON Schema object for arguments.
	Parameters() json.RawMessage
	Exec(ctx context.Context, args json.RawMessage) (string, error)
}

// Lifecycle hooks live in hooks.go (PreTool, PostTool, UserPrompt, …).

// Message is a chat message for injectable Chat funcs (tests / custom LLM).
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

// ChatFunc is an injectable LLM primitive (tests / custom backends).
type ChatFunc func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error)
