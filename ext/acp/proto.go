// Package acp implements a practical subset of the Agent Client Protocol (ACP)
// for mow: agent mode (stdio) and client mode (delegate to peer harnesses).
//
// Spec: https://agentclientprotocol.com (JSON-RPC 2.0, camelCase methods).
package acp

import (
	"encoding/json"
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
}

// sessionUpdate notification params.
type sessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"` // agent_message_chunk | …
	Content       *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

// Session modes advertised to the client (Zed mode switcher).
const (
	ModeCode = "code" // full tools per engine policy
	ModeAsk  = "ask"  // read-only tools for this session's prompts
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
