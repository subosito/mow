package mow

import (
	"context"
	"encoding/json"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/policy"
)

// IsPowerTool reports whether name is gated behind --allow-write/--allow-shell
// (write, edit, bash). Hosts building approval UIs should use this instead of
// hardcoding the list, so a new power tool cannot bypass their gate.
func IsPowerTool(name string) bool {
	return policy.IsPowerTool(name)
}

// ExtractThinking splits inline chain-of-thought wrappers (<think>…</think>
// and known dialects) out of answer text. unclosed reports an open tag with no
// close yet (still streaming). The agent loop already strips committed turns;
// this export is for UIs doing live-stream display.
func ExtractThinking(s string) (visible, thinking string, unclosed bool) {
	return agent.ExtractThinking(s)
}

// StripThinking is ExtractThinking for finished text (trims outer whitespace).
func StripThinking(s string) (visible, thinking string) {
	return agent.StripThinking(s)
}

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
	// StopReason and Usage are response-only metadata set by providers
	// ("max_tokens"/"length" = truncated; usage zero = not reported). They are
	// never serialized onto the wire.
	StopReason string `json:"-"`
	Usage      Usage  `json:"-"`
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
