package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// fsEngine builds an engine over ws with the given jail options, so fs/* goes
// through the same policy the file tools use.
func fsEngine(t *testing.T, ws string, opt mow.Options) *agentServer {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	opt.NoSession = true
	opt.Workspace = ws
	opt.Chat = func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "x"}, nil
	}
	eng, err := mow.New(opt)
	if err != nil {
		t.Fatal(err)
	}
	return &agentServer{eng: eng}
}

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFSPolicyReadOnlyWorkspace: a bare workspace jail ignored ReadOnly, so
// fs/write_text_file could edit the very workspace the operator marked
// read-only.
func TestFSPolicyReadOnlyWorkspace(t *testing.T) {
	ws := t.TempDir()
	rw := t.TempDir()
	writeFile(t, filepath.Join(rw, "existing.txt"), "extra\n")
	// Mirrors --read-only --extra-root PATH:rw: the path is a jail root (rw)
	// and the explicit writable allowlist for it.
	a := fsEngine(t, ws, mow.Options{ReadOnly: true, ExtraRoots: []string{rw}, WritableRoots: []string{rw}})

	if err := a.writeWorkspaceFile("primary.txt", []byte("nope")); err == nil {
		t.Fatal("write to the read-only primary workspace must be denied")
	}
	if _, err := os.Stat(filepath.Join(ws, "primary.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write still created a file: %v", err)
	}
	// The explicit :rw root stays writable under --read-only.
	if err := a.writeWorkspaceFile(filepath.Join(rw, "allowed.txt"), []byte("ok")); err != nil {
		t.Fatalf("write under the writable root: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(rw, "allowed.txt")); err != nil || string(b) != "ok" {
		t.Fatalf("writable-root content = %q, err=%v", b, err)
	}
}

// TestFSPolicyExtraRoots: reads outside the workspace were denied even when the
// host configured the directory as an extra jail root.
func TestFSPolicyExtraRoots(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	writeFile(t, filepath.Join(extra, "note.txt"), "extra-root-body\n")
	a := fsEngine(t, ws, mow.Options{ExtraRoots: []string{extra}})

	got, err := a.readWorkspaceFile(filepath.Join(extra, "note.txt"))
	if err != nil {
		t.Fatalf("read of an extra root: %v", err)
	}
	if strings.TrimSpace(got) != "extra-root-body" {
		t.Fatalf("extra-root content = %q", got)
	}
	// Files outside every configured root stay denied.
	if _, err := a.readWorkspaceFile(filepath.Join(t.TempDir(), "elsewhere.txt")); err == nil {
		t.Fatal("read outside the jail must be denied")
	}
}

// TestFSPolicyReadOnlyExtraRoot: an :ro extra root is readable but not
// writable, which the workspace-only jail could not express either.
func TestFSPolicyReadOnlyExtraRoot(t *testing.T) {
	ws := t.TempDir()
	roRoot := t.TempDir()
	writeFile(t, filepath.Join(roRoot, "pinned.txt"), "pinned\n")
	a := fsEngine(t, ws, mow.Options{ExtraRootsReadOnly: []string{roRoot}})

	if got, err := a.readWorkspaceFile(filepath.Join(roRoot, "pinned.txt")); err != nil || strings.TrimSpace(got) != "pinned" {
		t.Fatalf("read of read-only root = %q, err=%v", got, err)
	}
	if err := a.writeWorkspaceFile(filepath.Join(roRoot, "new.txt"), []byte("nope")); err == nil {
		t.Fatal("write under a read-only extra root must be denied")
	}
}
