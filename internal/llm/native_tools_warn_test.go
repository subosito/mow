package llm

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// Local native tools on chat-completions without catalog support: warn once
// (raw OpenAI gpt drops web_search silently). Catalog-listed tools must not warn.
func TestNativeToolsWarnOnChatCompletionsLocalOnly(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := &Client{
		Wire:        WireOpenAIChat,
		Model:       "warn-test-model",
		NativeTools: []map[string]any{{"type": "web_search"}},
	}
	raw, err := json.Marshal(map[string]any{"model": "warn-test-model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.finalizeChatBody(raw); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "native_tools may be dropped") {
		t.Fatalf("no warning for unsupported wire: %q", got)
	}

	// Warn once per model, not once per call: an agent loop makes many
	// requests and would otherwise repeat this on every one.
	buf.Reset()
	if _, err := c.finalizeChatBody(raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "native_tools may be dropped") {
		t.Fatalf("warning repeated on later calls: %q", buf.String())
	}
}

// Gateway models that advertise native_tools on chat-completions (Gemini via
// an OpenAI-compatible gateway, etc.) must not warn — the catalog is the capability claim.
func TestNativeToolsNoWarnWhenCatalogAdvertisesOnChat(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := &Client{
		Wire:  WireOpenAIChat,
		Model: "gemini-3.6-flash",
		CatalogModels: map[string]ModelInfo{
			"gemini-3.6-flash": {
				ID:          "gemini-3.6-flash",
				Wire:        WireOpenAIChat,
				NativeTools: []map[string]any{{"type": "web_search"}},
			},
		},
	}
	raw, _ := json.Marshal(map[string]any{"model": "gemini-3.6-flash"})
	if _, err := c.finalizeChatBody(raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "native_tools") {
		t.Fatalf("catalog-advertised tools must not warn: %q", buf.String())
	}
}

// Wires that do carry server tools must stay silent.
func TestNativeToolsNoWarnOnSupportedWires(t *testing.T) {
	for _, wire := range []string{WireOpenAIResponses, WireAnthropicMsg} {
		t.Run(wire, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			c := &Client{
				Wire:        wire,
				Model:       "nowarn-test-model-" + wire,
				NativeTools: []map[string]any{{"type": "web_search"}},
			}
			raw, _ := json.Marshal(map[string]any{"model": "nowarn-test-model-" + wire})
			if _, err := c.finalizeChatBody(raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(buf.String(), "native_tools") {
				t.Fatalf("%s warned but does carry server tools: %q", wire, buf.String())
			}
		})
	}
}
