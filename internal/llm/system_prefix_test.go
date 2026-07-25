package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEffectiveSystemPrefixModelGlobs(t *testing.T) {
	prefix := []string{"prefix-block-a"}
	// Empty patterns → always apply.
	if got := effectiveSystemPrefix(prefix, nil, "other-model"); len(got) != 1 {
		t.Fatalf("empty patterns should always apply: %v", got)
	}
	// family-* matches case-insensitively.
	if got := effectiveSystemPrefix(prefix, []string{"family-*"}, "Family-X-1"); len(got) != 1 {
		t.Fatalf("family-* should match Family-X-1: %v", got)
	}
	// Non-match drops prefix.
	if got := effectiveSystemPrefix(prefix, []string{"family-*"}, "other-model"); got != nil {
		t.Fatalf("non-match should not get prefix: %v", got)
	}
	// Empty prefix stays empty.
	if got := effectiveSystemPrefix(nil, []string{"family-*"}, "family-x"); got != nil {
		t.Fatalf("empty prefix: %v", got)
	}
}

func TestMessagesWithSystemPrefixOpenAI(t *testing.T) {
	c := &Client{
		Wire:               WireOpenAIChat,
		Model:              "family-x-1",
		SystemPrefix:       []string{"prefix-block-a"},
		SystemPrefixModels: []string{"family-*"},
	}
	got := c.messagesWithSystemPrefix([]Message{
		{Role: "system", Content: "main-system"},
		{Role: "user", Content: "hi"},
	})
	if len(got) != 3 || got[0].Role != "system" || got[0].Content != "prefix-block-a" {
		t.Fatalf("got=%+v", got)
	}
	if got[1].Content != "main-system" || got[2].Role != "user" {
		t.Fatalf("rest=%+v", got)
	}
	// Anthropic wire leaves messages unchanged (prefix is system field blocks).
	c.Wire = WireAnthropicMsg
	in := []Message{{Role: "user", Content: "hi"}}
	if out := c.messagesWithSystemPrefix(in); len(out) != 1 || out[0].Content != "hi" {
		t.Fatalf("anthropic passthrough=%+v", out)
	}
}

func TestChatOpenAISendsSystemPrefix(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := &Client{
		Wire: WireOpenAIChat, BaseURL: srv.URL + "/v1", APIKey: "k", Model: "family-x-1",
		HTTP: srv.Client(),
		SystemPrefix:       []string{"prefix-block-a"},
		SystemPrefixModels: []string{"family-*"},
	}
	if _, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "main-system"},
		{Role: "user", Content: "hi"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("messages=%v", gotBody["messages"])
	}
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "prefix-block-a" {
		t.Fatalf("messages[0]=%v", m0)
	}
}
