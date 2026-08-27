// Package acp implements a practical subset of the Agent Client Protocol (ACP)
// for mow: agent mode (stdio) and client mode (delegate to peer harnesses).
//
// Spec: https://agentclientprotocol.com (JSON-RPC 2.0, camelCase methods).
package acp

import (
	"encoding/json"
	"strings"
)

// ProtocolVersion is the ACP major version we negotiate.
const ProtocolVersion = 1

// JSON-RPC 2.0 envelope.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	errParse     = -32700
	errInvalid   = -32600
	errMethod    = -32601
	errInternal  = -32603
	errCancelled = -32800
)

// promptParams is session/prompt.
type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
	// Ephemeral is a mow extra: run against current context but do not
	// persist the turn. Generic ACP clients omit it (false).
	Ephemeral bool `json:"ephemeral"`
}

// sessionUpdate notification params.
type sessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

func (p *sessionUpdateParams) UnmarshalJSON(data []byte) error {
	var raw struct {
		SessionID     string          `json:"sessionId"`
		Update        json.RawMessage `json:"update"`
		SessionUpdate string          `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.SessionID = raw.SessionID
	if len(raw.Update) > 0 && string(raw.Update) != "null" {
		return json.Unmarshal(raw.Update, &p.Update)
	}
	// Flattened params: kind/title/content live next to sessionId.
	if raw.SessionUpdate != "" || strings.Contains(string(data), `"kind"`) {
		return json.Unmarshal(data, &p.Update)
	}
	return nil
}

type sessionUpdate struct {
	SessionUpdate string        `json:"sessionUpdate"` // agent_message_chunk | agent_thought_chunk | tool_call | …
	Content       *chunkContent `json:"content,omitempty"`
	// tool_call / tool_call_update (peer progress while delegated).
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	Locations  []toolLocation  `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
	// ToolContent is session/update content when the peer sent an array
	// (ACP ToolCallContent: diff / content / terminal). Message chunks stay
	// in Content as a single object.
	ToolContent []json.RawMessage `json:"-"`
}

type chunkContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolLocation struct {
	Path string `json:"path,omitempty"`
}

// UnmarshalJSON accepts content as either a single chunk object (message /
// thought) or a ToolCallContent array. An array used to fail the whole
// session/update and drop write/edit notifications from Cursor-class peers.
func (u *sessionUpdate) UnmarshalJSON(data []byte) error {
	var raw struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Update        json.RawMessage `json:"update"`
		Content       json.RawMessage `json:"content,omitempty"`
		ToolCallID    string          `json:"toolCallId,omitempty"`
		Title         string          `json:"title,omitempty"`
		Kind          string          `json:"kind,omitempty"`
		Status        string          `json:"status,omitempty"`
		Locations     []toolLocation  `json:"locations,omitempty"`
		RawInput      json.RawMessage `json:"rawInput,omitempty"`
		RawOutput     json.RawMessage `json:"rawOutput,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Some peers nest the tool_call under `update` even inside the already
	// nested sessionUpdateParams.Update. Flatten so kind/title/content land.
	if len(raw.Update) > 0 && string(raw.Update) != "null" && raw.Update[0] == '{' {
		var inner sessionUpdate
		if json.Unmarshal(raw.Update, &inner) == nil && inner.SessionUpdate != "" {
			*u = inner
			if raw.SessionUpdate != "" && u.SessionUpdate == "" {
				u.SessionUpdate = raw.SessionUpdate
			}
			return nil
		}
	}
	u.SessionUpdate = raw.SessionUpdate
	u.ToolCallID = raw.ToolCallID
	u.Title = raw.Title
	u.Kind = raw.Kind
	u.Status = raw.Status
	u.Locations = raw.Locations
	u.RawInput = raw.RawInput
	u.RawOutput = raw.RawOutput
	u.Content = nil
	u.ToolContent = nil
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	if raw.Content[0] == '[' {
		return json.Unmarshal(raw.Content, &u.ToolContent)
	}
	var c chunkContent
	if err := json.Unmarshal(raw.Content, &c); err != nil {
		return nil
	}
	u.Content = &c
	return nil
}

// Session modes advertised to the client (Zed mode switcher).
const (
	ModeCode = "code" // full tools per engine policy
	ModeAsk  = "ask"  // read-only tools for this session's prompts

	// ApprovalPrompt asks the editor before each power tool (default).
	// ApprovalAlways skips session/request_permission; still gated
	// by --allow-write / --allow-shell. Distinct from session mode.
	ApprovalPrompt = "prompt"
	ApprovalAlways = "always"
)

func availableModes() []map[string]any {
	return []map[string]any{
		{
			"id": ModeAsk, "name": "Ask",
			"description": "Read-only: no write/edit/bash for this session",
		},
		{
			"id": ModeCode, "name": "Code",
			"description": "Full tool access allowed by the mow process policy",
		},
	}
}

func modeState(current string) map[string]any {
	if current != ModeAsk && current != ModeCode {
		current = ModeCode
	}
	return map[string]any{
		"currentModeId":  current,
		"availableModes": availableModes(),
	}
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}
