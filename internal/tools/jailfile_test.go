package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

// TestOpenJailedSymlinkRace: between ResolvePath and open, replace an in-jail
// directory with a symlink to an outside tree. Without post-open verification
// the open would land outside and leak/write secrets; with it, the call fails
// and does not return outside content (or leave an outside write).
func TestOpenJailedSymlinkRace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("OUTSIDE-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	// In-jail path sub/secret.txt exists as a real file first so ResolvePath
	// succeeds; the hook then swaps sub/ for a symlink to outside.
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(sub, "secret.txt")
	if err := os.WriteFile(decoy, []byte("in-jail-decoy"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &policy.Policy{Workspace: ws, MaxReadBytes: 1 << 20, AllowWrite: true}

	// --- read race ---
	afterResolveHook = func(resolved string) {
		// Swap only once we have a resolved path under sub/.
		if err := os.Remove(decoy); err != nil {
			t.Errorf("remove decoy: %v", err)
			return
		}
		if err := os.Remove(sub); err != nil {
			t.Errorf("remove sub: %v", err)
			return
		}
		if err := os.Symlink(outside, sub); err != nil {
			t.Errorf("symlink sub->outside: %v", err)
		}
	}
	t.Cleanup(func() { afterResolveHook = nil })

	_, data, err := readFileJailed(p, "sub/secret.txt")
	if err == nil {
		t.Fatalf("read race: want jail error, got data %q", data)
	}
	if string(data) == "OUTSIDE-SECRET" {
		t.Fatal("read race: leaked outside content")
	}

	// Restore real sub/ for the write race (hook will swap again).
	_ = os.Remove(sub) // may be the symlink
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// --- write race: must not create outside/evil.txt ---
	evilOutside := filepath.Join(outside, "evil.txt")
	_ = os.Remove(evilOutside)
	afterResolveHook = func(resolved string) {
		if err := os.Remove(sub); err != nil {
			// sub may contain files from restore
			_ = os.RemoveAll(sub)
		}
		if err := os.Symlink(outside, sub); err != nil {
			t.Errorf("symlink sub->outside: %v", err)
		}
	}

	_, err = writeFileJailed(p, "sub/evil.txt", []byte("pwned"), 0o644)
	if err == nil {
		t.Fatal("write race: want jail error")
	}
	if b, rerr := os.ReadFile(evilOutside); rerr == nil {
		t.Fatalf("write race: outside file was written: %q", b)
	}

	// Sanity: normal in-jail read still works without the hook.
	afterResolveHook = nil
	if err := os.WriteFile(filepath.Join(ws, "ok.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, data, err = readFileJailed(p, "ok.txt")
	if err != nil || string(data) != "safe" {
		t.Fatalf("in-jail read: data=%q err=%v", data, err)
	}

	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Logf("note: post-open check on %s uses EvalSymlinks(name), weaker than /proc/self/fd or F_GETPATH", runtime.GOOS)
	}
}

func TestOpenJailedRejectsExistingSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(ws, "leak")); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws}
	// ResolvePath alone should already fail; openJailed must too.
	if _, _, err := openJailed(p, "leak", os.O_RDONLY, 0); err == nil {
		t.Fatal("expected symlink escape to fail")
	}
}
