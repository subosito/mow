package acp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func TestTerminalScrubsCredentials(t *testing.T) {
	// A terminal is a shell entry point: it must not hand the child the key
	// mow authenticates with (or the rest of the operator's credentials).
	t.Setenv("PROBE_API_KEY", "probe-key-secret")
	t.Setenv("PROBE_GITHUB_TOKEN", "probe-token-secret")
	t.Setenv("PROBE_API_TOKEN", "probe-token2-secret")
	t.Setenv("PROBE_PLAIN", "visible")
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
	term, err := a.createTerminal("s1", "bash", []string{"-c", "env; echo ENV-DONE"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer a.releaseTerm(term.id)
	out := drainTerminal(t, term, "ENV-DONE")
	if !strings.Contains(out, "PROBE_PLAIN=visible") {
		t.Fatalf("ordinary env must reach the terminal child: %q", out)
	}
	for _, secret := range []string{"probe-key-secret", "probe-token-secret", "probe-token2-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("credential %q leaked into terminal child: %q", secret, out)
		}
	}
}

// TestTerminalDeniesConfiguredCredential: the shape rule cannot see a gateway
// key with an arbitrary name, so the engine's configured api_key_env names are
// withheld explicitly.
func TestTerminalDeniesConfiguredCredential(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("PROBE_GATEWAY_KEY", "probe-gateway-secret")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("llm:\n  api_key_env: PROBE_GATEWAY_KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{
		NoSession:   true,
		AllowShell:  true,
		ConfigPaths: []string{cfgPath},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng, terms: map[string]*termSession{}}
	term, err := a.createTerminal("s1", "bash", []string{"-c", "env; echo ENV-DONE"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer a.releaseTerm(term.id)
	out := drainTerminal(t, term, "ENV-DONE")
	if got := eng.CredentialEnvNames(); len(got) != 1 || got[0] != "PROBE_GATEWAY_KEY" {
		t.Fatalf("CredentialEnvNames = %v", got)
	}
	if strings.Contains(out, "probe-gateway-secret") {
		t.Fatalf("configured credential leaked into terminal child: %q", out)
	}
	if strings.Contains(out, "PROBE_GATEWAY_KEY") {
		t.Fatalf("configured credential name leaked into terminal child: %q", out)
	}
}

// drainTerminal reads until the sentinel appears (the shell echoed it last).
func drainTerminal(t *testing.T, term *termSession, sentinel string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		out += term.takeOutput()
		if strings.Contains(out, sentinel) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("terminal produced no %q sentinel: %q", sentinel, out)
	return out
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
