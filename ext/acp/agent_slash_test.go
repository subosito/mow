package acp_test

import (
	"context"
	"encoding/json"
	"io"
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
		Name:    "acp-slash-ping",
		Summary: "ACP slash fixture",
		Usage:   "acp-slash-ping — fixture",
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
			if strings.Contains(u, "available_commands_update") && strings.Contains(u, "/acp-slash-ping") {
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
		t.Fatal("session/new did not advertise /acp-slash-ping")
	}

	stop, _, err := cl.prompt(ctx, sid, "/acp-slash-ping")
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
