package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

func TestApplyPatchExactAtomicAndJailed(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("one\ntwo\nthree\n"), 0o640)
	tool := &applyPatchTool{p: &policy.Policy{Workspace: root, AllowWrite: true}}
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	out, err := tool.Exec(t.Context(), mustPatchArgs(t, patch))
	if err != nil || !strings.Contains(out, "+TWO") {
		t.Fatalf("out=%s err=%v", out, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(data) != "one\nTWO\nthree\n" {
		t.Fatalf("data=%q", data)
	}
	st, _ := os.Stat(filepath.Join(root, "f.txt"))
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
	for _, bad := range []string{
		"--- a/f.txt\n+++ b/f.txt\n@@ -1,1 +1,1 @@\n-missing\n+x\n",
		"--- a/f.txt\n+++ b/g.txt\n@@ -1 +1 @@\n-one\n+x\n",
		"--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+x\n",
		"--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-one\n+x\n--- a/g.txt\n+++ b/g.txt\n@@ -1 +1 @@\n-a\n+b\n",
		"--- a/../escape\n+++ b/../escape\n@@ -1 +1 @@\n-a\n+b\n",
	} {
		before, _ := os.ReadFile(filepath.Join(root, "f.txt"))
		if _, err := tool.Exec(t.Context(), mustPatchArgs(t, bad)); err == nil {
			t.Fatalf("bad patch accepted: %q", bad)
		}
		after, _ := os.ReadFile(filepath.Join(root, "f.txt"))
		if string(before) != string(after) {
			t.Fatal("failed patch partially wrote")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), []byte{0xff, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPatch := "--- a/binary.txt\n+++ b/binary.txt\n@@ -1 +1 @@\n-a\n+b\n"
	if _, err := tool.Exec(t.Context(), mustPatchArgs(t, binaryPatch)); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("binary target=%v", err)
	}
}
func TestApplyPatchNeedsWrite(t *testing.T) {
	tool := &applyPatchTool{p: &policy.Policy{Workspace: t.TempDir()}}
	if _, err := tool.Exec(context.Background(), mustPatchArgs(t, "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")); err == nil {
		t.Fatal("write gate bypassed")
	}
}
func mustPatchArgs(t *testing.T, p string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"patch": p})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
