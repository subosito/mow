package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

func TestWriteFileJailedCreatesNestedParents(t *testing.T) {
	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws, AllowWrite: true}
	path, err := WriteFileJailed(p, "a/b/c.txt", []byte("ok"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "ok" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestWriteFileJailedDoesNotMkdirOutsideViaSymlinkParent(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(ws, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, AllowWrite: true}
	_, err := WriteFileJailed(p, "escape/nested/x.txt", []byte("nope"), 0o644)
	if err == nil {
		t.Fatal("expected jail error")
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); err == nil {
		t.Fatal("created directory outside the jail")
	}
}

// TestWriteFileJailedAtomic: the write must land via rename, not truncate in
// place, so a failure halfway (ENOSPC, kill) cannot leave an empty file. A
// hard link to the previous inode still holding the old bytes proves the
// target was replaced rather than overwritten. Mode bits of an existing file
// survive; a fresh file takes the requested perm.
func TestWriteFileJailedAtomic(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	tests := []struct {
		name     string
		existing string
		mode     os.FileMode
		perm     os.FileMode
		want     os.FileMode
	}{
		{name: "new file takes perm", perm: 0o644, want: 0o644},
		{name: "existing mode preserved", existing: "old", mode: 0o600, perm: 0o644, want: 0o600},
		{name: "existing exec mode preserved", existing: "old", mode: 0o755, perm: 0o644, want: 0o755},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			p := &policy.Policy{Workspace: ws, AllowWrite: true}
			target := filepath.Join(ws, "f.txt")
			link := filepath.Join(ws, "link.txt")
			if tc.existing != "" {
				if err := os.WriteFile(target, []byte(tc.existing), tc.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, tc.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, link); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := WriteFileJailed(p, "f.txt", []byte("new"), tc.perm); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "new" {
				t.Fatalf("content %q err=%v", got, err)
			}
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != tc.want {
				t.Fatalf("mode = %v, want %v", fi.Mode().Perm(), tc.want)
			}
			if tc.existing != "" {
				old, err := os.ReadFile(link)
				if err != nil || string(old) != tc.existing {
					t.Fatalf("old inode = %q err=%v, want %q (write was not atomic)", old, err, tc.existing)
				}
			}
			ents, err := os.ReadDir(ws)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range ents {
				if strings.Contains(e.Name(), ".tmp-") {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestWriteFileJailedRespectsUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			ws := t.TempDir()
			target := filepath.Join(ws, "file")
			want := os.FileMode(0o600)
			if existing {
				if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, 0o644); err != nil {
					t.Fatal(err)
				}
				want = 0o644
			}
			if _, err := WriteFileJailed(&policy.Policy{Workspace: ws}, "file", []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Fatalf("mode = %o, want %o under umask 077", fi.Mode().Perm(), want)
			}
		})
	}
}

func TestWriteFileJailedCommitParentSwap(t *testing.T) {
	for _, failRename := range []bool{false, true} {
		t.Run(fmt.Sprintf("failRename=%v", failRename), func(t *testing.T) {
			ws, outside := t.TempDir(), t.TempDir()
			sub, moved := filepath.Join(ws, "sub"), filepath.Join(ws, "moved")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sub, "file"), []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			outsideTarget := filepath.Join(outside, "file")
			if err := os.WriteFile(outsideTarget, []byte("outside target"), 0o600); err != nil {
				t.Fatal(err)
			}
			var outsideTemp string
			afterTempWriteHook = func(path string) {
				if failRename {
					if err := os.Remove(filepath.Join(sub, "file")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(sub, "file"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				outsideTemp = filepath.Join(outside, filepath.Base(path))
				if err := os.WriteFile(outsideTemp, []byte("outside temp"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(sub, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, sub); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { afterTempWriteHook = nil })
			_, err := WriteFileJailed(&policy.Policy{Workspace: ws}, "sub/file", []byte("new"), 0o644)
			if (err != nil) != failRename {
				t.Fatalf("write error = %v, want failure %v", err, failRename)
			}
			for path, want := range map[string]string{outsideTarget: "outside target", outsideTemp: "outside temp"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("outside file changed: %s = %q, err=%v", path, got, err)
				}
				fi, err := os.Stat(path)
				if err != nil || fi.Mode().Perm() != 0o600 {
					t.Fatalf("outside mode changed: %s, info=%v, err=%v", path, fi, err)
				}
			}
			if !failRename {
				got, err := os.ReadFile(filepath.Join(moved, "file"))
				if err != nil || string(got) != "new" {
					t.Fatalf("pinned target = %q, err=%v", got, err)
				}
			}
			entries, err := os.ReadDir(moved)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".tmp-") {
					t.Fatalf("temporary file left in pinned parent: %s", entry.Name())
				}
			}
		})
	}
}

func TestWriteFileJailedModeUsesPinnedParent(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "file")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	afterResolveHook = func(string) {
		if err := os.Rename(sub, filepath.Join(ws, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterResolveHook = nil })
	if _, err := WriteFileJailed(&policy.Policy{Workspace: ws}, "sub/file", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("used stale permissions: mode = %o, want 600", info.Mode().Perm())
	}
}
