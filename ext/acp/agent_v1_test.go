package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
	"github.com/subosito/mow/slash"
)

func TestAgentAdvertisesCommandsAndUsage(t *testing.T) {
	slash.Register(slash.Command{
		Name:    "acp-v1-ping",
		Summary: "ACP completeness fixture",
		Usage:   "acp-v1-ping — fixture",
		Run: func(ctx context.Context, req slash.Request) (slash.Result, error) {
			return slash.Result{Title: "pong", Body: "ok"}, nil
		},
	})

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hello-acp",
				Usage: mow.Usage{InputTokens: 12, OutputTokens: 3}}, nil
		},
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
	var mu sync.Mutex
	var updates []string
	cl.onNotify = func(method string, params json.RawMessage) {
		if method == "session/update" {
			mu.Lock()
			updates = append(updates, string(params))
			mu.Unlock()
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

	deadline := time.Now().Add(2 * time.Second)
	sawCmd := false
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, u := range updates {
			if strings.Contains(u, "available_commands_update") && strings.Contains(u, "/acp-v1-ping") {
				sawCmd = true
			}
		}
		mu.Unlock()
		if sawCmd {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawCmd {
		t.Fatal("session/new did not advertise /acp-v1-ping")
	}

	stop, _, err := cl.prompt(ctx, sid, "/acp-v1-ping")
	if err != nil {
		t.Fatalf("slash prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("slash stop=%q", stop)
	}
	mu.Lock()
	sawSlash := false
	for _, u := range updates {
		if strings.Contains(u, "agent_message_chunk") && strings.Contains(u, "pong") {
			sawSlash = true
			break
		}
	}
	mu.Unlock()
	if !sawSlash {
		t.Fatal("slash result not streamed as agent_message_chunk")
	}

	stop, usage, err := cl.prompt(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop=%q", stop)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 3 {
		t.Fatalf("usage=%+v", usage)
	}
	mu.Lock()
	sawUsage := false
	for _, u := range updates {
		if strings.Contains(u, "usage_update") && strings.Contains(u, "inputTokens") {
			sawUsage = true
			break
		}
	}
	mu.Unlock()
	if !sawUsage {
		t.Fatal("missing usage_update")
	}
	cancel()
	_ = aw.Close()
}

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
