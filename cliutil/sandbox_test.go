package cliutil

import (
	"flag"
	"io"
	"testing"
)

func parseSandbox(t *testing.T, args ...string) (*EngineFlags, error) {
	t.Helper()
	f := &EngineFlags{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, f.Validate()
}

func TestSandboxFlagDefaultsToNone(t *testing.T) {
	f, err := parseSandbox(t)
	if err != nil {
		t.Fatal(err)
	}
	if f.Sandbox != "" {
		t.Errorf("Sandbox = %q, want empty", f.Sandbox)
	}
	if got := f.Options().Sandbox; got != "" {
		t.Errorf("Options().Sandbox = %q, want empty (today's behavior)", got)
	}
}

func TestSandboxFlagAccepted(t *testing.T) {
	f, err := parseSandbox(t, "--allow-shell", "--sandbox", "bwrap")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Options().Sandbox; got != "bwrap" {
		t.Errorf("Options().Sandbox = %q, want bwrap", got)
	}
}

// A typo must never degrade into an unsandboxed shell.
func TestSandboxUnknownValueRejected(t *testing.T) {
	for _, v := range []string{"bwrapp", "docker", "landlock", "yes"} {
		if _, err := parseSandbox(t, "--allow-shell", "--sandbox", v); err == nil {
			t.Errorf("--sandbox %q: want error", v)
		}
	}
}

// Documented choice: --sandbox without --allow-shell is allowed and inert
// (nothing to jail), but the value is still validated.
func TestSandboxWithoutAllowShellIsAllowed(t *testing.T) {
	if _, err := parseSandbox(t, "--sandbox", "bwrap"); err != nil {
		t.Errorf("--sandbox without --allow-shell should be accepted: %v", err)
	}
	if _, err := parseSandbox(t, "--sandbox", "nonsense"); err == nil {
		t.Error("invalid --sandbox must error even without --allow-shell")
	}
}

func TestSandboxNoneIsExplicitlyValid(t *testing.T) {
	if _, err := parseSandbox(t, "--allow-shell", "--sandbox", "none"); err != nil {
		t.Errorf("--sandbox none should be valid: %v", err)
	}
}
