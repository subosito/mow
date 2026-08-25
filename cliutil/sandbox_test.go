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
	if runtime.GOOS != "linux" {
		t.Skip("flag not registered off linux")
	}
	f, err := parseSandbox(t, "--allow-shell", "--sandbox=bwrap")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Options().Sandbox; got != "bwrap" {
		t.Errorf("Options().Sandbox = %q, want bwrap", got)
	}
}

// Bare --sandbox (bool-style) opts in without spelling out bwrap.
func TestSandboxFlagBareMeansBwrap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flag not registered off linux")
	}
	f, err := parseSandbox(t, "--allow-shell", "--sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Options().Sandbox; got != "bwrap" {
		t.Errorf("Options().Sandbox = %q, want bwrap", got)
	}
}

// Explicit opt-out uses the =form (bool-flag parsing: "--sandbox none" would
// mean bare --sandbox plus a positional "none").
func TestSandboxFlagExplicitDisable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flag not registered off linux")
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
	if runtime.GOOS != "linux" {
		t.Skip("flag not registered off linux")
	}
	for _, v := range []string{"bwrapp", "docker", "landlock", "yes"} {
		if _, err := parseSandbox(t, "--allow-shell", "--sandbox="+v); err == nil {
			t.Errorf("--sandbox=%s: want error", v)
		}
	}
}

// Documented choice: --sandbox without --allow-shell is allowed and inert
// (nothing to jail), but the value is still validated.
func TestSandboxWithoutAllowShellIsAllowed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flag not registered off linux")
	}
	if _, err := parseSandbox(t, "--sandbox"); err != nil {
		t.Errorf("--sandbox without --allow-shell should be accepted: %v", err)
	}
	if _, err := parseSandbox(t, "--sandbox=nonsense"); err == nil {
		t.Error("invalid --sandbox value must error even without --allow-shell")
	}
}

func TestSandboxNoneIsExplicitlyValid(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flag not registered off linux")
	}
	if _, err := parseSandbox(t, "--allow-shell", "--sandbox=none"); err != nil {
		t.Errorf("--sandbox=none should be valid: %v", err)
	}
}

// Off-Linux the flag is not registered at all, so --sandbox is an
// "unknown flag" parse error — never a silent unsandboxed shell.
func TestSandboxBwrapRejectedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux advertises bwrap")
	}
	if _, err := parseSandbox(t, "--allow-shell", "--sandbox=bwrap"); err == nil {
		t.Fatal("--sandbox=bwrap must error off linux")
	}
	if _, err := parseSandbox(t, "--allow-shell", "--sandbox"); err == nil {
		t.Fatal("bare --sandbox must error off linux")
	}
}
