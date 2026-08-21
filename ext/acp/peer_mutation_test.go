package acp

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

func TestPeerFileMutationFromKindAndTitle(t *testing.T) {
	m, ok := peerFileMutation(sessionUpdate{Kind: "edit", Title: "src/foo.rs", Status: "in_progress", ToolCallID: "c1"})
	if !ok || m.Tool != "edit" || m.Path != "src/foo.rs" || m.Done {
		t.Fatalf("mow-shaped edit: %+v ok=%v", m, ok)
	}
	m, ok = peerFileMutation(sessionUpdate{Kind: "write", Title: "notes.md", Status: "completed"})
	if !ok || m.Tool != "write" || m.Path != "notes.md" || !m.Done {
		t.Fatalf("write title path: %+v ok=%v", m, ok)
	}
	if _, ok := peerFileMutation(sessionUpdate{Kind: "read", Title: "src/foo.rs"}); ok {
		t.Fatal("read must not be a file mutation")
	}
	if _, ok := peerFileMutation(sessionUpdate{Kind: "edit", Title: "Search files"}); ok {
		t.Fatal("edit without a path must be skipped")
	}
}

func TestPeerFileMutationFromLocationsAndDiff(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "tc",
		"kind":          "edit",
		"status":        "completed",
		"locations":     []map[string]any{{"path": "internal/x.go"}},
		"content": []map[string]any{{
			"type":    "diff",
			"path":    "internal/x.go",
			"oldText": "old",
			"newText": "new",
		}},
	})
	var u sessionUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	m, ok := peerFileMutation(u)
	if !ok || m.Path != "internal/x.go" || m.Tool != "edit" || !m.Done {
		t.Fatalf("locations edit: %+v ok=%v", m, ok)
	}
	if !looksUnifiedDiff(m.Diff) || !strings.Contains(m.Diff, "internal/x.go") {
		t.Fatalf("diff=%q", m.Diff)
	}
	if !strings.Contains(m.Diff, "-old") || !strings.Contains(m.Diff, "+new") {
		t.Fatalf("diff body=%q", m.Diff)
	}
}

func TestSessionUpdateContentArrayDoesNotDropToolCall(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"sessionId": "s",
		"update": map[string]any{
			"sessionUpdate": "tool_call",
			"kind":          "edit",
			"title":         "src/app.rs",
			"status":        "in_progress",
			"content":       []map[string]any{{"type": "diff", "path": "src/app.rs", "oldText": "a", "newText": "b"}},
		},
	})
	var p sessionUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		t.Fatalf("content array must parse: %v", err)
	}
	if p.Update.Kind != "edit" || p.Update.Title != "src/app.rs" {
		t.Fatalf("update=%+v", p.Update)
	}
	if len(p.Update.ToolContent) != 1 {
		t.Fatalf("ToolContent=%d", len(p.Update.ToolContent))
	}
}

func TestOnNotificationFileMutationIncludesPath(t *testing.T) {
	c := &Client{}
	var mu sync.Mutex
	var muts []fileMutation
	c.SetOnFileMutation(func(m fileMutation) {
		mu.Lock()
		muts = append(muts, m)
		mu.Unlock()
	})
	emit := func(update map[string]any) {
		params, _ := json.Marshal(map[string]any{"sessionId": "s", "update": update})
		c.onNotification(notification{Method: "session/update", Params: params})
	}
	emit(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "1",
		"kind":          "edit",
		"title":         "src/foo.rs",
		"status":        "in_progress",
	})
	emit(map[string]any{
		"sessionUpdate": "tool_call",
		"kind":          "read",
		"title":         "src/foo.rs",
		"status":        "in_progress",
	})
	emit(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "1",
		"kind":          "edit",
		"locations":     []map[string]any{{"path": "src/foo.rs"}},
		"status":        "completed",
		"content": []map[string]any{{
			"type":    "diff",
			"path":    "src/foo.rs",
			"oldText": "fn a() {}",
			"newText": "fn b() {}",
		}},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(muts) != 2 {
		t.Fatalf("muts=%+v", muts)
	}
	if muts[0].Path != "src/foo.rs" || muts[0].Tool != "edit" || muts[0].Done {
		t.Fatalf("start=%+v", muts[0])
	}
	if muts[1].Path != "src/foo.rs" || !muts[1].Done || !looksUnifiedDiff(muts[1].Diff) {
		t.Fatalf("end=%+v", muts[1])
	}
}

func TestEmitHostFileMutationOnEvent(t *testing.T) {
	var evs []mow.Event
	eng, err := mow.New(mow.Options{
		NoSession: true,
		DeferLLM:  true,
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventToolStart || ev.Type == mow.EventToolEnd {
				evs = append(evs, ev)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	started := map[string]bool{}
	emitHostFileMutation(eng, "cursor", fileMutation{
		CallID: "tc-1", Tool: "edit", Path: "src/foo.rs",
	}, started)
	emitHostFileMutation(eng, "cursor", fileMutation{
		CallID: "tc-1", Tool: "edit", Path: "src/foo.rs",
		Done: true,
		Diff: "--- src/foo.rs\n+++ src/foo.rs\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}, started)

	if len(evs) != 2 {
		t.Fatalf("events=%d %+v", len(evs), evs)
	}
	if evs[0].Type != mow.EventToolStart || evs[0].Tool != "edit" {
		t.Fatalf("start=%+v", evs[0])
	}
	if evs[1].Type != mow.EventToolEnd || evs[1].Tool != "edit" {
		t.Fatalf("end=%+v", evs[1])
	}
	var args map[string]string
	if err := json.Unmarshal(evs[0].Args, &args); err != nil || args["path"] != "src/foo.rs" {
		t.Fatalf("args=%s err=%v", evs[0].Args, err)
	}
	if evs[1].Path != "src/foo.rs" || !strings.Contains(evs[1].Result, "+new") {
		t.Fatalf("end payload=%+v", evs[1])
	}
	if !strings.HasPrefix(evs[0].ToolCallID, "acp:cursor:") {
		t.Fatalf("tool_call_id=%q", evs[0].ToolCallID)
	}
}

func TestEmitHostFileMutationWhenDiffArrivesInProgress(t *testing.T) {
	var evs []mow.Event
	eng, err := mow.New(mow.Options{
		NoSession: true,
		DeferLLM:  true,
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventToolStart || ev.Type == mow.EventToolEnd {
				evs = append(evs, ev)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	started := map[string]bool{}
	diff := "--- a.rs\n+++ a.rs\n@@ -1 +1 @@\n-old\n+new\n"
	emitHostFileMutation(eng, "cursor", fileMutation{
		CallID: "x", Tool: "edit", Path: "a.rs", Diff: diff,
	}, started)
	if len(evs) != 2 {
		t.Fatalf("want start+end on in-progress diff, got %d %+v", len(evs), evs)
	}
	if evs[1].Type != mow.EventToolEnd || !strings.Contains(evs[1].Result, "+new") {
		t.Fatalf("end=%+v", evs[1])
	}
}

func TestFlattenedSessionUpdateParams(t *testing.T) {
	raw := []byte(`{"sessionId":"s","sessionUpdate":"tool_call","kind":"edit","title":"src/x.rs","status":"in_progress","content":[{"type":"diff","path":"src/x.rs","oldText":"a","newText":"b"}]}`)
	var p sessionUpdateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Update.Kind != "edit" || p.Update.Title != "src/x.rs" {
		t.Fatalf("update=%+v", p.Update)
	}
	m, ok := peerFileMutation(p.Update)
	if !ok || m.Path != "src/x.rs" || m.Diff == "" {
		t.Fatalf("mut=%+v ok=%v", m, ok)
	}
}
