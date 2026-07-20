package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamHooksSeparatesReasoningAndContent(t *testing.T) {
	// Simulate DeepSeek/ZenMux: long reasoning then short content.
	const body = "" +
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"think \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"step\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept=%q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "deepseek-v4-flash",
		HTTP:    srv.Client(),
	}

	var content, reasoning strings.Builder
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{
		OnContent:   func(d string) { content.WriteString(d) },
		OnReasoning: func(d string) { reasoning.WriteString(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hi!" {
		t.Fatalf("Message.Content=%q want Hi! (reasoning must not leak)", msg.Content)
	}
	if content.String() != "Hi!" {
		t.Fatalf("content callbacks=%q", content.String())
	}
	if reasoning.String() != "think step" {
		t.Fatalf("reasoning callbacks=%q", reasoning.String())
	}
}

func TestChatStreamHooksReasoningContentField(t *testing.T) {
	const body = "" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
	var reason strings.Builder
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnReasoning: func(d string) { reason.WriteString(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "ok" {
		t.Fatalf("content=%q", msg.Content)
	}
	if reason.String() != "plan" {
		t.Fatalf("reason=%q", reason.String())
	}
}
