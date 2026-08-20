package rpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func dummyChat(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
	return mow.Message{Role: "assistant", Content: "ok"}, nil
}

func TestRPCServerValidationAndDefaults(t *testing.T) {
	t.Parallel()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat:      dummyChat,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("nil engine", func(t *testing.T) {
		t.Parallel()
		srv := &Server{Out: &bytes.Buffer{}}
		if err := srv.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "nil engine") {
			t.Fatalf("expected nil engine error, got %v", err)
		}
	})

	t.Run("nil out", func(t *testing.T) {
		t.Parallel()
		srv := &Server{Engine: eng}
		if err := srv.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "nil out") {
			t.Fatalf("expected nil out error, got %v", err)
		}
	})

	t.Run("nil in defaults to empty reader", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		srv := &Server{Engine: eng, Out: &out}
		if err := srv.Serve(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestRPCSessionAndMethods(t *testing.T) {
	t.Parallel()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "test-model",
		Chat:      dummyChat,
	})
	if err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader(
		"\n" + // empty line test
			`{"id":10,"method":"session"}` + "\n" +
			`{"id":11,"method":"session_id"}` + "\n",
	)
	var out bytes.Buffer
	srv := &Server{Engine: eng, In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	res := out.String()
	if !strings.Contains(res, `"session_id"`) || !strings.Contains(res, `"model":"test-model"`) {
		t.Fatalf("unexpected session output: %s", res)
	}
	if !strings.Contains(res, `"extra_roots"`) ||
		!strings.Contains(res, `"extra_roots_rw"`) ||
		!strings.Contains(res, `"extra_roots_ro"`) {
		t.Fatalf("session missing extra-root metadata: %s", res)
	}
	if strings.Contains(res, `"git"`) {
		t.Fatalf("session must not expose Git presentation metadata: %s", res)
	}
}

func TestRPCHandlePromptError(t *testing.T) {
	t.Parallel()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{}, errors.New("simulated chat failure")
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader(`{"id":1,"method":"prompt","params":{"text":"hello"}}` + "\n")
	var out bytes.Buffer
	srv := &Server{Engine: eng, In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	s := out.String()
	if !strings.Contains(s, "-32603") || !strings.Contains(s, "simulated chat failure") {
		t.Fatalf("expected internal error code -32603 and error message, got: %s", s)
	}
}

func TestRPCStreamEventsDisabled(t *testing.T) {
	t.Parallel()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat:      dummyChat,
	})
	if err != nil {
		t.Fatal(err)
	}

	noStream := false
	in := strings.NewReader(`{"id":1,"method":"prompt","params":{"text":"hello"}}` + "\n")
	var out bytes.Buffer
	srv := &Server{Engine: eng, In: in, Out: &out, StreamEvents: &noStream}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	s := out.String()
	if strings.Contains(s, `"method":"event"`) {
		t.Fatalf("expected no event notifications when StreamEvents is false, got: %s", s)
	}
	if !strings.Contains(s, `"text":"ok"`) {
		t.Fatalf("expected prompt response, got: %s", s)
	}
}

func TestRPCContextCancellation(t *testing.T) {
	t.Parallel()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat:      dummyChat,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r, w := bytesNewPipeInternal()
	defer w.Close()

	var out bytes.Buffer
	srv := &Server{Engine: eng, In: r, Out: &out}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return on canceled context")
	}
}

func TestRPCVeryLongMessage(t *testing.T) {
	t.Parallel()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "received long text"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	longText := strings.Repeat("a", 100*1024) // 100 KB text
	reqPayload := `{"id":1,"method":"prompt","params":{"text":"` + longText + `"}}` + "\n"
	in := strings.NewReader(reqPayload)
	var out bytes.Buffer

	srv := &Server{Engine: eng, In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "received long text") {
		t.Fatalf("expected long text response, got %s", out.String())
	}
}

func TestRPCCommandHelpAndArgs(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"-h", "--help", "help"} {
		if code := runCmd([]string{flag}); code != 0 {
			t.Errorf("runCmd(%q) = %d; want 0", flag, code)
		}
	}

	if code := runCmd([]string{"--invalid-flag-xyz"}); code != 2 {
		t.Errorf("runCmd(invalid flag) = %d; want 2", code)
	}

	// DeferLLM: ping/version must start without an API key.
	if code := runCmd([]string{"--no-session"}); code != 0 {
		t.Errorf("runCmd(--no-session) = %d; want 0 (DeferLLM)", code)
	}
}

type internalPipeReader struct {
	ch  chan []byte
	buf []byte
}

func (r *internalPipeReader) Read(p []byte) (int, error) {
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

type internalPipeWriter struct {
	ch chan []byte
}

func (w *internalPipeWriter) WriteString(s string) (int, error) {
	w.ch <- []byte(s)
	return len(s), nil
}

func (w *internalPipeWriter) Close() error {
	close(w.ch)
	return nil
}

func bytesNewPipeInternal() (*internalPipeReader, *internalPipeWriter) {
	ch := make(chan []byte, 16)
	return &internalPipeReader{ch: ch}, &internalPipeWriter{ch: ch}
}
