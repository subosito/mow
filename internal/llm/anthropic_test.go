package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicSystemFieldPrefixBlocks(t *testing.T) {
	// No prefix, no cache → plain string (third-party hosts).
	if got := anthropicSystemField(nil, "persona", false); got != "persona" {
		t.Fatalf("plain=%v", got)
	}
	// Prefix + persona → separate blocks (not one concatenated string).
	got := anthropicSystemField([]string{
		"prefix-block-a",
		"", // dropped
	}, "main-system", false)
	blocks, ok := got.([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("blocks=%T %#v", got, got)
	}
	if blocks[0]["text"] != "prefix-block-a" {
		t.Fatalf("system[0]=%v", blocks[0]["text"])
	}
	if blocks[1]["text"] != "main-system" {
		t.Fatalf("system[1]=%v", blocks[1]["text"])
	}
	if _, isStr := got.(string); isStr {
		t.Fatal("prefix must not collapse to a string")
	}
	// Cache marks every block.
	cached := anthropicSystemField([]string{"pre"}, "body", true)
	cblocks := cached.([]map[string]any)
	for i, b := range cblocks {
		if b["cache_control"] == nil {
			t.Fatalf("block %d missing cache_control", i)
		}
	}
}

func TestChatAnthropicSendsSystemPrefix(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := &Client{
		Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "family-x-1",
		HTTP: srv.Client(), PromptCache: false,
		SystemPrefix:       []string{"prefix-block-a"},
		SystemPrefixModels: []string{"family-*"},
	}
	if _, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "main-system"},
		{Role: "user", Content: "hi"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sys, ok := gotBody["system"].([]any)
	if !ok || len(sys) != 2 {
		t.Fatalf("system=%T %#v", gotBody["system"], gotBody["system"])
	}
	b0 := sys[0].(map[string]any)
	if b0["text"] != "prefix-block-a" {
		t.Fatalf("system[0]=%v", b0["text"])
	}
	b1 := sys[1].(map[string]any)
	if b1["text"] != "main-system" {
		t.Fatalf("system[1]=%v", b1["text"])
	}

	// Same prefix config, non-matching model → plain system string, no preamble.
	c.Model = "other-model"
	gotBody = nil
	if _, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "main-system"},
		{Role: "user", Content: "hi"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if s, ok := gotBody["system"].(string); !ok || s != "main-system" {
		t.Fatalf("non-match system=%T %#v", gotBody["system"], gotBody["system"])
	}
}

func TestToAnthropicMessages(t *testing.T) {
	sys, msgs := toAnthropicMessages([]Message{
		{Role: "system", Content: "be nice"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", ToolCalls: []ToolCall{
			{ID: "1", Type: "function", Function: FunctionCall{Name: "read", Arguments: `{"path":"a"}`}},
		}},
		{Role: "tool", ToolCallID: "1", Content: "file"},
	})
	if sys != "be nice" {
		t.Fatalf("sys=%q", sys)
	}
	if len(msgs) < 3 {
		t.Fatalf("msgs=%d", len(msgs))
	}
}

func TestChatAnthropicStopReasonAndMaxTokens(t *testing.T) {
	// Non-stream path: stop_reason must surface on the Message, and MaxTokens
	// must reach the request body (defaulting to 8192 when zero).
	cases := []struct {
		name          string
		maxTokens     int
		wantMaxTokens float64
		stopReason    string
		wantTruncated bool
	}{
		{"default max_tokens, truncated", 0, 8192, "max_tokens", true},
		{"custom max_tokens, end_turn", 512, 512, "end_turn", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"partial"}],"stop_reason":"` + tc.stopReason + `"}`))
			}))
			t.Cleanup(srv.Close)

			c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), MaxTokens: tc.maxTokens}
			msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := gotBody["max_tokens"]; got != tc.wantMaxTokens {
				t.Fatalf("request max_tokens=%v want %v", got, tc.wantMaxTokens)
			}
			if msg.Content != "partial" {
				t.Fatalf("content=%q", msg.Content)
			}
			if msg.StopReason != tc.stopReason || msg.Truncated() != tc.wantTruncated {
				t.Fatalf("StopReason=%q Truncated=%v want %q/%v", msg.StopReason, msg.Truncated(), tc.stopReason, tc.wantTruncated)
			}
		})
	}
}
