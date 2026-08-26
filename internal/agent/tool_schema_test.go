package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// mcpStyleTool mimics what an MCP server registers: an ordinary JSON Schema,
// document metadata included. mow's built-in tools are hand-written and clean,
// so only third-party tools ever carried these keys — which is why this went
// unnoticed until a strict provider rejected the request.
type mcpStyleTool struct{}

func (mcpStyleTool) Name() string        { return "ctx_execute" }
func (mcpStyleTool) Description() string { return "run code" }
func (mcpStyleTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"properties": {
			"code": {"type": "string"},
			"opts": {"$id": "opts-v1", "type": "object",
				"properties": {"lang": {"type": "string"}}}
		},
		"required": ["code"]
	}`)
}
func (mcpStyleTool) Exec(context.Context, json.RawMessage) (string, error) { return "", nil }

// A tool schema carrying JSON Schema metadata must reach the wire without it.
// A strict provider fails the *entire* request over one such key, so a single
// third-party tool takes every other tool down with it:
//
//	HTTP 400: Unknown name "$schema" at
//	'request.tools[0].function_declarations[17].parameters'
func TestToolSchemasReachWireSanitized(t *testing.T) {
	var sent []llm.ToolSpec
	chat := func(_ context.Context, _ []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		sent = append([]llm.ToolSpec(nil), tools...)
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}

	_, err := agent.Run(t.Context(), chat, "go", agent.Options{
		Tools: []agent.Tool{mcpStyleTool{}, echoTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("got %d tool specs, want 2", len(sent))
	}

	for _, spec := range sent {
		params := string(spec.Function.Parameters)
		for _, bad := range []string{"$schema", "$id"} {
			if strings.Contains(params, bad) {
				t.Errorf("%s: %s reached the wire: %s", spec.Function.Name, bad, params)
			}
		}
		// Sanitizing must not gut the schema: the model still needs to know
		// what the parameters are.
		if !strings.Contains(params, `"type"`) {
			t.Errorf("%s: schema lost its type: %s", spec.Function.Name, params)
		}
	}

	// Spot-check that the substance survived, not just that the keys are gone.
	var mcp string
	for _, s := range sent {
		if s.Function.Name == "ctx_execute" {
			mcp = string(s.Function.Parameters)
		}
	}
	for _, want := range []string{"code", "opts", "lang", "required"} {
		if !strings.Contains(mcp, want) {
			t.Errorf("sanitize dropped %q from the schema: %s", want, mcp)
		}
	}
}

// A tool with no parameters still needs a valid object schema, and sanitizing
// must not turn that default into something a provider rejects.
type noParamTool struct{}

func (noParamTool) Name() string                                          { return "ping" }
func (noParamTool) Description() string                                   { return "ping" }
func (noParamTool) Parameters() json.RawMessage                           { return nil }
func (noParamTool) Exec(context.Context, json.RawMessage) (string, error) { return "pong", nil }

func TestEmptyToolSchemaStaysValid(t *testing.T) {
	var sent []llm.ToolSpec
	chat := func(_ context.Context, _ []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		sent = tools
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	if _, err := agent.Run(t.Context(), chat, "go", agent.Options{
		Tools: []agent.Tool{noParamTool{}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("got %d specs, want 1", len(sent))
	}
	var v map[string]any
	if err := json.Unmarshal(sent[0].Function.Parameters, &v); err != nil {
		t.Fatalf("empty-param schema is not valid JSON (%q): %v",
			sent[0].Function.Parameters, err)
	}
	if v["type"] != "object" {
		t.Errorf("want an object schema, got %s", sent[0].Function.Parameters)
	}
}
