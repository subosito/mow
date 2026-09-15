package cliutil

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestTrustCommandHelpUsesHostName(t *testing.T) {
	out := captureStderr(t, func() {
		if code := TrustCommand("mowi", []string{"help"}); code != 0 {
			t.Fatalf("code=%d", code)
		}
	})
	for _, want := range []string{"mowi trust —", "mowi trust --list", "mowi trust --revoke"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

func TestTrustCommandGrantListRevoke(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	workspace := t.TempDir()
	captureStderr(t, func() {
		if code := TrustCommand("mowi", []string{workspace}); code != 0 {
			t.Fatalf("grant code=%d", code)
		}
	})
	out := captureStdout(t, func() {
		if code := TrustCommand("mowi", []string{"--list"}); code != 0 {
			t.Fatalf("list code=%d", code)
		}
	})
	if !strings.Contains(out, workspace) {
		t.Fatalf("list missing workspace %q: %s", workspace, out)
	}
	captureStderr(t, func() {
		if code := TrustCommand("mowi", []string{"--revoke", workspace}); code != 0 {
			t.Fatalf("revoke code=%d", code)
		}
	})
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return captureFile(t, &os.Stderr, fn)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return captureFile(t, &os.Stdout, fn)
}

func captureFile(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := *target
	*target = w
	fn()
	_ = w.Close()
	*target = old
	var b bytes.Buffer
	_, _ = io.Copy(&b, r)
	_ = r.Close()
	return b.String()
}

// A group/other-readable trust file is ignored by TrustedWorkspaces, so
// granting through it would silently drop every existing entry: refuse.
func TestTrustCommandRefusesLooseTrustFilePerms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	path := home + "/trusted"
	if err := os.WriteFile(path, []byte("/old/workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStderr(t, func() { code = TrustCommand("mow", []string{t.TempDir()}) })
	if code != 1 || !strings.Contains(out, "chmod 600") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "/old/workspace\n" {
		t.Fatalf("trust file rewritten: %q %v", data, err)
	}
}
