package acp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// TestTerminalRunsUnderSandbox is the live-Fence check that terminal/create is
// actually jailed (see internal/tools/fence_test.go for the bash/proc pair).
// Gated like those: it needs the Fence CLI and bubblewrap.
func TestTerminalRunsUnderSandbox(t *testing.T) {
	if os.Getenv("MOW_TEST_FENCE") != "1" {
		t.Skip("set MOW_TEST_FENCE=1 for live Fence test")
	}
	for _, bin := range []string{"fence", "bwrap"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		AllowShell: true,
		Sandbox:    "fence",
		Workspace:  t.TempDir(),
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ShellSandbox(); err != nil {
		t.Fatalf("sandbox backend: %v", err)
	}
	a := &agentServer{eng: eng, terms: map[string]*termSession{}}
	term, err := a.createTerminal("s1", "bash", []string{"-c", "echo FENCE=$FENCE_SANDBOX; echo DONE-MARK"}, 80, 24)
	if err != nil {
		t.Fatalf("createTerminal under sandbox: %v", err)
	}
	defer a.releaseTerm(term.id)
	out := drainTerminal(t, term, "DONE-MARK")
	// Fence exports FENCE_SANDBOX=1 into the jailed command; without the wrap
	// the PTY child would run on the host.
	if !strings.Contains(out, "FENCE=1") {
		t.Fatalf("terminal child was not sandboxed: %q", out)
	}
}
