package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
