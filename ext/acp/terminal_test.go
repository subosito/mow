package acp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestTerminalPTY(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		AllowShell: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng, terms: map[string]*termSession{}}
	term, err := a.createTerminal("s1", "bash", []string{"-c", "echo hello-mow-pty; sleep 0.1"}, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer a.releaseTerm(term.id)

	deadline := time.Now().Add(2 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		out += term.takeOutput()
		if strings.Contains(out, "hello-mow-pty") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out, "hello-mow-pty") {
		t.Fatalf("output=%q", out)
	}
	select {
	case <-term.exitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
	}
}

func TestTerminalPushNotifications(t *testing.T) {
	var buf bytes.Buffer
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		AllowShell: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng, out: &buf, terms: map[string]*termSession{}}
	term, err := a.createTerminal("sess-1", "bash", []string{"-c", "printf 'push-chunk\\n'; sleep 0.05"}, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer a.releaseTerm(term.id)

	select {
	case <-term.exitCh:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit")
	}
	// Allow pushExit to flush.
	time.Sleep(50 * time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, `"sessionUpdate":"terminal_output"`) && !strings.Contains(out, `"sessionUpdate": "terminal_output"`) {
		// json.Encoder may omit spaces; check both compact shapes via field names.
		if !strings.Contains(out, "terminal_output") {
			t.Fatalf("missing terminal_output push: %s", out)
		}
	}
	if !strings.Contains(out, "terminal_exit") {
		t.Fatalf("missing terminal_exit push: %s", out)
	}
	if !strings.Contains(out, "sess-1") {
		t.Fatalf("missing sessionId: %s", out)
	}
	if !strings.Contains(out, "push-chunk") {
		t.Fatalf("missing output data: %s", out)
	}
	if !strings.Contains(out, term.id) {
		t.Fatalf("missing terminalId: %s", out)
	}
}
