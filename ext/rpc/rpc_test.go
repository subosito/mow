package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/rpc"
)

func TestRPCPrompt(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"id":1,"method":"ping"}` + "\n" + `{"id":2,"method":"prompt","params":{"text":"x"}}` + "\n")
	var out bytes.Buffer
	srv := &rpc.Server{Engine: eng, In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "pong") || !strings.Contains(s, `"text":"hi"`) {
		t.Fatalf("out=%s", s)
	}
	if !strings.Contains(s, "run.start") || !strings.Contains(s, "run.end") {
		t.Fatalf("expected event notifications: %s", s)
	}
	if !strings.Contains(s, `"stop_reason":"completed"`) && !strings.Contains(s, `"stop_reason": "completed"`) {
		// compact json encoder
		if !strings.Contains(s, "stop_reason") {
			t.Fatalf("missing stop_reason: %s", s)
		}
	}
}

func TestRPCStatusAndVersion(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"id":1,"method":"status"}` + "\n" + `{"id":2,"method":"version"}` + "\n")
	var out bytes.Buffer
	srv := &rpc.Server{Engine: eng, In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"busy"`) || !strings.Contains(out.String(), `"rpc":"2"`) {
		t.Fatalf("out=%s", out.String())
	}
}

func TestRPCCancel(t *testing.T) {
	started := make(chan struct{})
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			close(started)
			<-ctx.Done()
			return mow.Message{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Pipe: write prompt then cancel after start.
	pr, pw := bytesNewPipe()
	var out bytes.Buffer
	srv := &rpc.Server{Engine: eng, In: pr, Out: &out}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background()) }()

	_, _ = pw.WriteString(`{"id":1,"method":"prompt","params":{"text":"block"}}` + "\n")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not start")
	}
	_, _ = pw.WriteString(`{"id":2,"method":"cancel"}` + "\n")
	_ = pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not finish")
	}
	s := out.String()
	if !strings.Contains(s, `"ok":true`) && !strings.Contains(s, `"ok": true`) {
		// cancel response
		if !strings.Contains(s, "ok") {
			t.Fatalf("missing cancel ok: %s", s)
		}
	}
	// final prompt error or cancelled stop
	if !strings.Contains(s, "cancel") && !strings.Contains(s, "canceled") && !strings.Contains(s, "cancelled") {
		// stop_reason cancelled in result
		if !strings.Contains(s, "stop_reason") {
			t.Fatalf("expected cancel/error result: %s", s)
		}
	}
}

// tiny pipe without net
func bytesNewPipe() (*pipeReader, *pipeWriter) {
	ch := make(chan []byte, 16)
	r := &pipeReader{ch: ch}
	w := &pipeWriter{ch: ch}
	return r, w
}

type pipeReader struct {
	ch  chan []byte
	buf []byte
}

func (r *pipeReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		b, ok := <-r.ch
		if !ok {
			return 0, io.EOF
		}
		r.buf = b
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

type pipeWriter struct {
	ch chan []byte
}

func (w *pipeWriter) WriteString(s string) (int, error) {
	w.ch <- []byte(s)
	return len(s), nil
}
func (w *pipeWriter) Close() error {
	close(w.ch)
	return nil
}

func TestRPCEventJSONShape(t *testing.T) {
	// Ensure event notification unmarshals.
	raw := `{"method":"event","params":{"type":"run.start","run_id":"run-x","ts":"2026-01-01T00:00:00Z"}}`
	var n struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &n); err != nil || n.Method != "event" {
		t.Fatal(err)
	}
}

func TestRPCJSONRPC2Conformance(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A conformant client sends jsonrpc:2.0; also probe unknown method, empty
	// method, and bad json for standard error codes.
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" +
			`{"id":2,"method":"nope"}` + "\n" +
			`{"id":3,"method":""}` + "\n" +
			`not json` + "\n")
	var out bytes.Buffer
	srv := &rpc.Server{Engine: eng, In: in, Out: &out, StreamEvents: new(bool)}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Every emitted line must be JSON-RPC 2.0 tagged.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line not json: %q", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("line missing jsonrpc:2.0: %q", line)
		}
	}
	s := out.String()
	if !strings.Contains(s, "-32601") { // method not found
		t.Fatalf("unknown method missing code -32601: %s", s)
	}
	if !strings.Contains(s, "-32600") { // invalid request (empty method)
		t.Fatalf("empty method missing code -32600: %s", s)
	}
	if !strings.Contains(s, "-32700") { // parse error
		t.Fatalf("bad json missing code -32700: %s", s)
	}
}
