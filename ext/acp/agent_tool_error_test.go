package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

func TestAgentToolFailureCarriesErrorWithoutReadBody(t *testing.T) {
	var eng *mow.Engine
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		eng.Emit(mow.Event{Type: mow.EventToolEnd, Tool: "read", ToolCallID: "r1", Args: json.RawMessage(`{"path":"photo.png"}`), Error: "read photo.png: image files are binary; attach the image"})
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { _ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw}); _ = cw.Close() }()
	cl := newPipeClient(cr, aw)
	updates := make(chan map[string]any, 1)
	cl.onNotify = func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var p map[string]any
		if json.Unmarshal(params, &p) != nil {
			return
		}
		u, _ := p["update"].(map[string]any)
		if u != nil && u["sessionUpdate"] == "tool_call_update" {
			updates <- u
		}
	}
	go cl.readLoop()
	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "inspect"); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-updates:
		if u["status"] != "failed" || u["error"] != "read photo.png: image files are binary; attach the image" {
			t.Fatalf("update=%v", u)
		}
		if _, ok := u["rawOutput"]; ok {
			t.Fatalf("read body must stay omitted: %v", u)
		}
	case <-ctx.Done():
		t.Fatal("missing tool failure update")
	}
}
