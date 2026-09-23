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

func TestAgentAskUserRoundTrip(t *testing.T) {
	calls := 0
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(_ context.Context, m []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
		calls++
		if calls == 1 {
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{ID: "q", Function: mow.FunctionCall{Name: "ask_user", Arguments: `{"questions":[{"id":"pkg","prompt":"Choose","options":[{"id":"a","label":"A"}]}]}`}}}}, nil
		}
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
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		if method != "session/request_input" {
			t.Errorf("method=%s", method)
			return
		}
		_ = cl.reply(id, map[string]any{"answers": []map[string]any{{"id": "pkg", "selected": []string{"a"}}}})
	}
	go cl.readLoop()
	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "ask"); err != nil {
		t.Fatal(err)
	}
}
