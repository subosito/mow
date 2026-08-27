package acp_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

func TestSessionNewRefusesSecondActive(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()
	cl := newPipeClient(cr, aw)
	go cl.readLoop()
	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	first, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("empty sessionId")
	}
	if _, err := cl.sessionNew(ctx, t.TempDir()); err == nil {
		t.Fatal("second session/new succeeded")
	} else if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("second new err=%v", err)
	}
	cancel()
	_ = aw.Close()
}

func TestSessionCloseThenNewIsFresh(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()
	cl := newPipeClient(cr, aw)
	go cl.readLoop()
	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	first, err := cl.sessionNew(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.prompt(ctx, first, "hi"); err != nil {
		t.Fatal(err)
	}
	if err := cl.callOK(ctx, "session/close", map[string]any{"sessionId": first}); err != nil {
		t.Fatal(err)
	}
	second, err := cl.sessionNew(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("close+new reused %q", first)
	}
	cancel()
	_ = aw.Close()
}

func TestSessionLoadOtherIdRefused(t *testing.T) {
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()
	cl := newPipeClient(cr, aw)
	go cl.readLoop()
	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.call(ctx, "session/load", map[string]any{"sessionId": "not-this-engine"}); err == nil {
		t.Fatal("load of other id succeeded")
	} else if !strings.Contains(err.Error(), "restart mow acp") && !strings.Contains(err.Error(), "holds session") {
		t.Fatalf("load err=%v", err)
	}
	cancel()
	_ = aw.Close()
}
