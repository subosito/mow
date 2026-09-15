package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

type blockedConfigProvider struct {
	catalogStarted chan struct{}
	chatStarted    chan struct{}
	chatCancelled  chan struct{}
	once           sync.Once
}

func (p *blockedConfigProvider) ListModels(ctx context.Context) ([]mow.ModelInfo, error) {
	p.once.Do(func() { close(p.catalogStarted) })
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *blockedConfigProvider) SetModel(string) error { return nil }
func (p *blockedConfigProvider) Chat(ctx context.Context, _ []mow.Message, _ []mow.ToolSpec, _ mow.ChatHooks) (mow.Message, error) {
	close(p.chatStarted)
	<-ctx.Done()
	close(p.chatCancelled)
	return mow.Message{}, ctx.Err()
}

func TestSlowModelChangeKeepsPingAndCancelResponsive(t *testing.T) {
	prov := &blockedConfigProvider{catalogStarted: make(chan struct{}), chatStarted: make(chan struct{}), chatCancelled: make(chan struct{})}
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Model: "gpt-5-mini", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	defer ar.Close()
	defer aw.Close()
	defer cr.Close()
	defer cw.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw}) }()
	cl := newPipeClient(cr, aw)
	go cl.readLoop()
	// session/new binds before fetching the catalog. Do not wait for its
	// response: the deliberately blocked catalog is independent of prompting.
	if err := json.NewEncoder(aw).Encode(map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session/new", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prov.catalogStarted:
	case <-time.After(time.Second):
		t.Fatal("catalog did not start")
	}
	promptDone := make(chan error, 1)
	go func() { _, _, err := cl.prompt(ctx, "mow-1", "hello"); promptDone <- err }()
	select {
	case <-prov.chatStarted:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	if err := json.NewEncoder(aw).Encode(map[string]any{
		"jsonrpc": "2.0", "id": "change", "method": "session/set_config_option",
		"params": map[string]any{"sessionId": "mow-1", "configId": "model", "value": "gpt-5-mini"},
	}); err != nil {
		t.Fatal(err)
	}
	pingCtx, stopPing := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stopPing()
	if err := cl.callOK(pingCtx, "ping", nil); err != nil {
		t.Fatalf("slow model change blocked ping: %v", err)
	}
	if err := cl.notify("session/cancel", map[string]any{"sessionId": "mow-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prov.chatCancelled:
	case <-time.After(time.Second):
		t.Fatal("slow model change blocked cancellation")
	}
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatalf("cancel response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no prompt cancellation response")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("catalog work did not stop on connection cancellation")
	}
}
