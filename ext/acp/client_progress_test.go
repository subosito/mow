package acp

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestFormatPeerToolProgress(t *testing.T) {
	cases := []struct {
		u    sessionUpdate
		want string
	}{
		{sessionUpdate{Kind: "read", Title: "engine.go", Status: "pending"}, "read engine.go"},
		{sessionUpdate{Kind: "bash", Title: "bash", Status: "completed"}, "bash ✓"},
		{sessionUpdate{Title: "Search files", Status: "failed"}, "Search files ✗"},
		{sessionUpdate{Kind: "edit", Status: "in_progress"}, "edit"},
		{sessionUpdate{}, ""},
	}
	for _, c := range cases {
		got := formatPeerToolProgress(c.u)
		if got != c.want {
			t.Fatalf("formatPeerToolProgress(%+v)=%q want %q", c.u, got, c.want)
		}
	}
}

func TestOnNotificationProgressAndChunk(t *testing.T) {
	c := &Client{}
	var mu sync.Mutex
	var chunks, progress []string
	c.SetOnChunk(func(d string) {
		mu.Lock()
		chunks = append(chunks, d)
		mu.Unlock()
	})
	c.SetOnProgress(func(kind, text string) {
		mu.Lock()
		progress = append(progress, kind+":"+text)
		mu.Unlock()
	})

	emit := func(update map[string]any) {
		params, _ := json.Marshal(map[string]any{"sessionId": "s", "update": update})
		c.onNotification(notification{Method: "session/update", Params: params})
	}

	emit(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "hello"},
	})
	emit(map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": " thinking "},
	})
	emit(map[string]any{
		"sessionUpdate": "tool_call",
		"kind":          "read",
		"title":         "foo.go",
		"status":        "pending",
	})
	emit(map[string]any{
		"sessionUpdate": "tool_call_update",
		"kind":          "read",
		"title":         "foo.go",
		"status":        "completed",
	})

	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Fatalf("chunks=%v", chunks)
	}
	// Reply buffer only has answer text.
	c.textMu.Lock()
	reply := c.text.String()
	c.textMu.Unlock()
	if reply != "hello" {
		t.Fatalf("reply buffer=%q", reply)
	}
	if len(progress) != 3 {
		t.Fatalf("progress=%v", progress)
	}
	if progress[0] != "thought:thinking" {
		t.Fatalf("thought=%q", progress[0])
	}
	if progress[1] != "tool:read foo.go" {
		t.Fatalf("tool start=%q", progress[1])
	}
	if progress[2] != "tool:read foo.go ✓" {
		t.Fatalf("tool end=%q", progress[2])
	}
}
