package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/slash"
)

func TestExclusiveSlashForwardsGoalBeforeResponse(t *testing.T) {
	name := "test-goal-progress"
	release := make(chan struct{})
	slash.Register(slash.Command{Name: name, Exclusive: true, Run: func(_ context.Context, req slash.Request) (slash.Result, error) {
		req.Engine.Emit(mow.Event{Type: mow.EventGoalStart, Goal: &mow.GoalEvent{ID: "g", Status: "running", Phase: "planning"}})
		<-release
		return slash.Result{Body: "done"}, nil
	}})
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) { return mow.Message{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	reader, writer := io.Pipe()
	a := &agentServer{eng: eng, out: writer, boundSID: "s", sessions: map[string]*acpSession{"s": {mode: ModeCode}}, cancels: map[string]context.CancelFunc{}}
	done := make(chan struct{})
	go func() {
		a.handleSlashExtra(t.Context(), request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "slash", Params: mustJSON(map[string]any{"name": name})})
		close(done)
		_ = writer.Close()
	}()
	sc := bufio.NewScanner(reader)
	lines := make(chan map[string]json.RawMessage, 2)
	go func() {
		for sc.Scan() {
			var msg map[string]json.RawMessage
			if json.Unmarshal(sc.Bytes(), &msg) == nil {
				lines <- msg
			}
		}
	}()
	select {
	case first := <-lines:
		var method string
		_ = json.Unmarshal(first["method"], &method)
		if method != "session/update" {
			t.Fatalf("first message is %q, want progress notification", method)
		}
		close(release)
	case <-time.After(time.Second):
		t.Fatal("goal progress did not arrive before response")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slash did not finish")
	}
}
