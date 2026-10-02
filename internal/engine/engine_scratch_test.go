package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionScratchIsolationResumeAndRotation(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	workspace := t.TempDir()
	options := Options{LoadUserConfig: true, Workspace: workspace, SessionID: "scratch-test", Chat: profileChat()}
	eng, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	scratch := eng.scratchDir
	note := filepath.Join(scratch, "artifact.txt")
	if _, err := eng.WriteFile(note, []byte("no")); err == nil {
		t.Fatal("scratch bypassed write permission")
	}
	if err := os.WriteFile(note, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	resumed, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.scratchDir != scratch {
		t.Fatal("resume did not reuse scratch")
	}
	if _, data, err := resumed.ReadFile(note); err != nil || string(data) != "artifact" {
		t.Fatalf("scratch read: %q, %v", data, err)
	}
	if _, err := eng.BeginSession(); err != nil {
		t.Fatal(err)
	}
	if eng.scratchDir == scratch {
		t.Fatal("new session reused scratch")
	}
	if _, _, err := eng.ReadFile(note); err == nil {
		t.Fatal("new session can read previous scratch")
	}
	if strings.Contains(eng.sys, scratch) {
		t.Fatal("system retained previous scratch root")
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatal("rotation deleted scratch artifact")
	}
	options.AllowWrite = true
	writable, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if _, err := writable.WriteFile(note, []byte("updated")); err != nil {
		t.Fatal(err)
	}
}

func TestSessionScratchRejectsSymlinksAndRecreatesMissingDirectory(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	workspace := t.TempDir()
	root, err := sessionScratch(workspace, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	recreated, err := sessionScratch(workspace, "test")
	if err != nil || recreated != root {
		t.Fatalf("recreate: %s %v", recreated, err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Skip(err)
	}
	if _, err := sessionScratch(workspace, "test"); err == nil {
		t.Fatal("accepted symlink scratch root")
	}
}
