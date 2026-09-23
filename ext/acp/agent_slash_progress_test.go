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

// TestExclusiveSlashStreamsLiveProgress: a goal drives engine turns inside a
// slash command. Its tool calls, model text, and file changes must reach the
// host while the command runs (like a prompt), not only goal-level events.
func TestExclusiveSlashStreamsLiveProgress(t *testing.T) {
	name := "test-goal-live"
	release := make(chan struct{})
	slash.Register(slash.Command{Name: name, Exclusive: true, Run: func(_ context.Context, req slash.Request) (slash.Result, error) {
		req.Engine.Emit(mow.Event{Type: mow.EventToolStart, Tool: "read", ToolCallID: "c1", Args: json.RawMessage(`{"path":"main.go"}`)})
		req.Engine.Emit(mow.Event{Type: mow.EventFSChange, Files: []mow.FileChange{{Path: "main.go", Op: "write"}}})
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
	updates := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(reader)
		for sc.Scan() {
			var msg struct {
				Method string `json:"method"`
				Params struct {
					Update struct {
						SessionUpdate string `json:"sessionUpdate"`
					} `json:"update"`
				} `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Method == "session/update" {
				updates <- msg.Params.Update.SessionUpdate
			}
		}
		close(updates)
	}()
	want := map[string]bool{"tool_call": false, "fs_change": false}
	deadline := time.After(2 * time.Second)
	for !want["tool_call"] || !want["fs_change"] {
		select {
		case kind := <-updates:
			if _, ok := want[kind]; ok {
				want[kind] = true
			}
		case <-deadline:
			t.Fatalf("live progress missing before slash response: %v", want)
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slash did not finish")
	}
	// The token callback is released when the command ends, so a later
	// prompt's streaming is not routed through this finished forwarder.
	if eng.Status().Busy {
		t.Fatal("engine still busy after slash")
	}
}
