package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

func TestAgentRequestPermissionAllow(t *testing.T) {
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		Workspace:  dir,
		AllowWrite: true,
		Chat:       writeChat("note.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	cl := newPipeClient(cr, aw)
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		if method != "session/request_permission" {
			return
		}
		var p struct {
			Options []struct {
				OptionID string `json:"optionId"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &p)
		opt := "allow_once"
		for _, o := range p.Options {
			if o.OptionID == "allow_once" {
				opt = o.OptionID
				break
			}
		}
		_ = cl.reply(id, map[string]any{
			"outcome": map[string]any{"outcome": "selected", "optionId": opt},
		})
	}
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "write it"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err != nil {
		t.Fatalf("allowed write missing: %v", err)
	}
	cancel()
	_ = aw.Close()
}

func TestAgentRequestPermissionReject(t *testing.T) {
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		Workspace:  dir,
		AllowWrite: true,
		Chat:       writeChat("note.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	cl := newPipeClient(cr, aw)
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		if method != "session/request_permission" {
			return
		}
		_ = cl.reply(id, map[string]any{
			"outcome": map[string]any{"outcome": "selected", "optionId": "reject_once"},
		})
	}
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "write it"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err == nil {
		t.Fatal("rejected write still landed")
	}
	cancel()
	_ = aw.Close()
}

func TestAgentRequestPermissionRejectAlways(t *testing.T) {
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		Workspace:  dir,
		AllowWrite: true,
		Chat:       writeEveryUserTurn("note.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	var asks atomic.Int32
	cl := newPipeClient(cr, aw)
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		if method != "session/request_permission" {
			return
		}
		asks.Add(1)
		_ = cl.reply(id, map[string]any{
			"outcome": map[string]any{"outcome": "selected", "optionId": "reject_always"},
		})
	}
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "write first"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if _, _, err := cl.prompt(ctx, sid, "write second"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	if n := asks.Load(); n != 1 {
		t.Fatalf("permission asks=%d want 1 (reject_always should stick)", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err == nil {
		t.Fatal("rejected write still landed")
	}
	cancel()
	_ = aw.Close()
}

func TestAgentRequestPermissionMissingClientDenies(t *testing.T) {
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		Workspace:  dir,
		AllowWrite: true,
		Chat:       writeChat("note.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	cl := newPipeClient(cr, aw)
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		if method != "session/request_permission" {
			return
		}
		_ = cl.replyError(id, -32601, "Method not found")
	}
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "write it"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err == nil {
		t.Fatal("write landed without a permission client")
	}
	cancel()
	_ = aw.Close()
}

func TestAgentApprovalsAlwaysSkipsOverlay(t *testing.T) {
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		Workspace:  dir,
		AllowWrite: true,
		Chat:       writeChat("note.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	var asks atomic.Int32
	cl := newPipeClient(cr, aw)
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		if method == "session/request_permission" {
			asks.Add(1)
			_ = cl.reply(id, map[string]any{
				"outcome": map[string]any{"outcome": "selected", "optionId": "reject_once"},
			})
		}
	}
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	sid, err := cl.sessionNew(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.callOK(ctx, "session/set_config_option", map[string]any{
		"sessionId": sid,
		"configId":  "approvals",
		"value":     "always",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, sid, "write it"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if asks.Load() != 0 {
		t.Fatalf("approvals=always still asked %d times", asks.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err != nil {
		t.Fatalf("write missing under approvals always: %v", err)
	}
	cancel()
	_ = aw.Close()
}

func writeEveryUserTurn(path string) func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
	var n int
	return func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		if len(messages) == 0 {
			return mow.Message{Role: "assistant", Content: "empty"}, nil
		}
		if messages[len(messages)-1].Role == "tool" {
			return mow.Message{Role: "assistant", Content: "denied"}, nil
		}
		n++
		args, _ := json.Marshal(map[string]string{"path": path, "content": "hello\n"})
		return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
			ID: "call-" + strconv.Itoa(n), Type: "function",
			Function: mow.FunctionCall{Name: "write", Arguments: string(args)},
		}}}, nil
	}
}

func writeChat(path string) func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
	var step int
	return func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		step++
		if step == 1 {
			args, _ := json.Marshal(map[string]string{"path": path, "content": "hello\n"})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "call-1", Type: "function",
				Function: mow.FunctionCall{Name: "write", Arguments: string(args)},
			}}}, nil
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "tool" {
				return mow.Message{Role: "assistant", Content: "tool:" + messages[i].Content}, nil
			}
		}
		return mow.Message{Role: "assistant", Content: "no tool result"}, nil
	}
}
