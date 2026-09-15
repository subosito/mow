package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestAgentOutputFailureCancelsConnection(t *testing.T) {
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(), NoSession: true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	in, client := io.Pipe()
	defer in.Close()
	defer client.Close()
	broken := errors.New("test output disconnected")
	done := make(chan error, 1)
	go func() {
		done <- Agent(t.Context(), AgentOptions{Engine: eng, In: in, Out: promptResponseWriter(func([]byte) (int, error) {
			return 0, broken
		})})
	}()
	if _, err := io.WriteString(client, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, broken) {
			t.Fatalf("Agent error=%v, want output failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent kept serving after output disconnected")
	}
}

func TestAgentOutputFailureCancelsPrompt(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(), NoSession: true,
		Chat: func(ctx context.Context, _ []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return mow.Message{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	in, client := io.Pipe()
	defer in.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	broken := errors.New("test output disconnected")
	a := &agentServer{
		eng: eng, boundSID: "session", cancels: map[string]context.CancelFunc{},
		out: promptResponseWriter(func([]byte) (int, error) { return 0, broken }),
	}
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx, in) }()
	enc := json.NewEncoder(client)
	if err := enc.Encode(request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "session/prompt", Params: mustJSON(promptParams{
		SessionID: "session", Prompt: []ContentBlock{{Type: "text", Text: "hello"}},
	})}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	if err := enc.Encode(request{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "ping"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("broken output left provider running")
	}
	select {
	case err := <-done:
		if !errors.Is(err, broken) {
			t.Fatalf("connection error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent did not exit")
	}
}

func TestCallClientFailsOnBrokenOutput(t *testing.T) {
	broken := errors.New("test broken pipe")
	a := &agentServer{out: promptResponseWriter(func([]byte) (int, error) { return 0, broken })}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := a.callClient(ctx, "session/request_permission", map[string]any{})
	if !errors.Is(err, broken) {
		t.Fatalf("callClient error=%v, want write failure, not permission timeout", err)
	}
	if len(a.pending) != 0 {
		t.Fatal("permission waiter leaked")
	}
}

func TestClientRejectsCallsAfterStdoutEOF(t *testing.T) {
	c := &Client{stdout: io.NopCloser(strings.NewReader("")), stdin: discardWriteCloser{}}
	c.readLoop()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := c.call(ctx, "ping", nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "stdout") {
		t.Fatalf("call after EOF=%v, want immediate connection failure", err)
	}
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

func TestClientDuplicateRepliesDoNotBlockReader(t *testing.T) {
	ch := make(chan response, 1)
	// The waiter has not consumed its first reply; duplicate IDs must not
	// block unrelated notifications and responses on the connection.
	line := "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{}}\n"
	c := &Client{stdout: io.NopCloser(strings.NewReader(strings.Repeat(line, 3))), pending: map[string]chan response{"1": ch}}
	done := make(chan struct{})
	go func() { c.readLoop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		// Unblock the pre-fix reader before failing the test.
		for range 3 {
			select {
			case <-ch:
			case <-done:
			}
		}
		t.Fatal("duplicate response blocked the reader")
	}
	got := <-ch
	if got.Error != nil || string(got.ID) != `"1"` {
		t.Fatalf("first response was lost: %+v", got)
	}
}

func TestClientResponseBeforeExitWins(t *testing.T) {
	for range 100 {
		exited := make(chan struct{})
		c := &Client{exited: exited, pending: map[string]chan response{}}
		c.stdin = &replyThenExitWriter{client: c, exited: exited}
		got, err := c.call(t.Context(), "ping", nil)
		if err != nil || string(got) != `"pong"` {
			t.Fatalf("delivered response lost to process exit: %s, %v", got, err)
		}
	}
}

type replyThenExitWriter struct {
	client *Client
	exited chan struct{}
}

func (w *replyThenExitWriter) Write(p []byte) (int, error) {
	var req request
	if err := json.Unmarshal(p, &req); err != nil {
		return 0, err
	}
	var id string
	_ = json.Unmarshal(req.ID, &id)
	w.client.pendMu.Lock()
	w.client.pending[id] <- response{ID: req.ID, Result: "pong"}
	w.client.pendMu.Unlock()
	close(w.exited)
	return len(p), nil
}
func (*replyThenExitWriter) Close() error { return nil }
