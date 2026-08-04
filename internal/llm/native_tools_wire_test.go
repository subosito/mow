package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeToolsReachTheWire(t *testing.T) {
	for _, wire := range []string{"openai-chat-completions", "openai-responses", "anthropic-messages"} {
		t.Run(wire, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.Header().Set("Content-Type", "application/json")
				switch wire {
				case "anthropic-messages":
					_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
				case "openai-responses":
					_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
				default:
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
				}
			}))
			defer srv.Close()

			c := &Client{
				Wire: wire, BaseURL: srv.URL, APIKey: "k", Model: "m",
				NativeTools: []map[string]any{{"type": "web_search"}},
			}
			_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			tools, _ := got["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("%s: native tool not on the wire: %#v", wire, got["tools"])
			}
			m, _ := tools[0].(map[string]any)
			if m["type"] != "web_search" {
				t.Fatalf("%s: wrong declaration: %#v", wire, tools[0])
			}
		})
	}
}
