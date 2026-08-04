package llm

import (
	"encoding/json"
	"testing"
)

// Native tools are provider-executed declarations merged into the request
// "tools" array. Verified against a live gateway: a chat model with
// tools:[{"type":"web_search"}] performs real searches, so the passthrough is
// the supported way to reach provider search — not a per-facet model id.
func TestFinalizeChatBodyMergesNativeTools(t *testing.T) {
	c := &Client{
		Model:       "test-model",
		NativeTools: []map[string]any{{"type": "web_search"}},
	}
	raw, err := json.Marshal(map[string]any{"model": "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("want 1 native tool, got %#v", got["tools"])
	}
	if m, _ := tools[0].(map[string]any); m["type"] != "web_search" {
		t.Fatalf("wrong tool declared: %#v", tools[0])
	}
}

// The agent's own function tools share the array with native ones. Replacing
// instead of appending would strand the loop with no local tools at all.
func TestFinalizeChatBodyKeepsLocalTools(t *testing.T) {
	c := &Client{
		Model:       "test-model",
		NativeTools: []map[string]any{{"type": "web_search"}},
	}
	raw, err := json.Marshal(map[string]any{
		"model": "test-model",
		"tools": []any{map[string]any{"type": "function", "name": "read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("want local + native, got %#v", tools)
	}
	first, _ := tools[0].(map[string]any)
	if first["name"] != "read" {
		t.Fatalf("local tool lost or reordered: %#v", tools)
	}
}

// No config → nothing added. A body that would otherwise be untouched must
// stay byte-identical so plain providers never see an unexpected field.
func TestFinalizeChatBodyNoNativeToolsIsNoop(t *testing.T) {
	c := &Client{Model: "test-model"}
	raw, err := json.Marshal(map[string]any{"model": "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Fatalf("body changed with no native tools:\n got %s\nwant %s", out, raw)
	}
}

func TestMergeNativeToolsDedupesByType(t *testing.T) {
	existing := []any{map[string]any{"type": "web_search"}}
	got := mergeNativeTools(existing, []map[string]any{
		{"type": "web_search"},
		{"type": "x_search"},
	})
	if len(got) != 2 {
		t.Fatalf("want dedupe to 2 entries, got %#v", got)
	}
}
