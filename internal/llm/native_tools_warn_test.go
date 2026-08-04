package llm

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// chat-completions has no server-tool channel. Verified against a live
// gateway: gpt-5.x with tools:[{"type":"web_search"}] on this wire answers
// from training data and never searches, with no error — while grok-4.5 on
// openai-responses searches and cites. Silent staleness is the worst failure
// mode here, so mow warns rather than letting it look like it worked.
func TestNativeToolsWarnOnChatCompletions(t *testing.T) {
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
	if !strings.Contains(got, "native_tools ignored") {
		t.Fatalf("no warning for unsupported wire: %q", got)
	}

	// Warn once per model, not once per call: an agent loop makes many
	// requests and would otherwise repeat this on every one.
	buf.Reset()
	if _, err := c.finalizeChatBody(raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "native_tools ignored") {
		t.Fatalf("warning repeated on later calls: %q", buf.String())
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
			if strings.Contains(buf.String(), "native_tools ignored") {
				t.Fatalf("%s warned but does carry server tools: %q", wire, buf.String())
			}
		})
	}
}
