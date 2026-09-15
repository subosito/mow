package cliutil

import (
	"flag"
	"io"
	"runtime"
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
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("flag not registered on unsupported OS")
	}
	f, err := parseSandbox(t, "--allow-shell", "--sandbox=fence")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Options().Sandbox; got != "fence" {
		t.Errorf("Options().Sandbox = %q, want fence", got)
	}
}

// Bare --sandbox (bool-style) opts in without spelling out fence.
func TestSandboxFlagBareMeansFence(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("flag not registered on unsupported OS")
	}
	f, err := parseSandbox(t, "--allow-shell", "--sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Options().Sandbox; got != "fence" {
		t.Errorf("Options().Sandbox = %q, want fence", got)
	}
}

// Explicit opt-out uses the =form (bool-flag parsing: "--sandbox none" would
// mean bare --sandbox plus a positional "none").
func TestSandboxFlagExplicitDisable(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("flag not registered on unsupported OS")
	}
	for _, args := range [][]string{
		{"--allow-shell", "--sandbox=none"},
		{"--allow-shell", "--sandbox=false"},
	} {
		f, err := parseSandbox(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if got := f.Options().Sandbox; got != "none" && got != "" {
			t.Errorf("%v: Sandbox = %q, want none/empty", args, got)
		}
	}
}

// A typo must never degrade into an unsandboxed shell.
func TestSandboxUnknownValueRejected(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("flag not registered on unsupported OS")
	}
	for _, v := range []string{"bwrap", "bubblewrap", "fencep", "docker", "landlock", "yes"} {
		if _, err := parseSandbox(t, "--allow-shell", "--sandbox="+v); err == nil {
			t.Errorf("--sandbox=%s: want error", v)
		}
	}
}

// Documented choice: --sandbox without --allow-shell is allowed and inert
// (nothing to jail), but the value is still validated.
func TestSandboxWithoutAllowShellIsAllowed(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("flag not registered on unsupported OS")
	}
	if _, err := parseSandbox(t, "--sandbox"); err != nil {
		t.Errorf("--sandbox without --allow-shell should be accepted: %v", err)
	}
	if _, err := parseSandbox(t, "--sandbox=nonsense"); err == nil {
		t.Error("invalid --sandbox value must error even without --allow-shell")
	}
}

func TestSandboxNoneIsExplicitlyValid(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("flag not registered on unsupported OS")
	}
	if _, err := parseSandbox(t, "--allow-shell", "--sandbox=none"); err != nil {
		t.Errorf("--sandbox=none should be valid: %v", err)
	}
}

func TestSandboxFenceFlag(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unsupported OS")
	}
	f, err := parseSandbox(t, "--allow-shell", "--sandbox=fence")
	if err != nil {
		t.Fatal(err)
	}
	if f.Options().Sandbox != "fence" {
		t.Fatalf("lost fence backend: %q", f.Options().Sandbox)
	}
}
