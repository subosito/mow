package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestToolResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, ID: "20260101T000000"}

	id, err := s.SaveToolResult("Bash", "needle-alpha\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	if !toolResultIDPattern.MatchString(id) {
		t.Fatalf("id %q does not match tool-result pattern", id)
	}
	if !strings.Contains(id, "-bash-") {
		t.Fatalf("id %q does not contain sanitized tool name", id)
	}
	got, err := s.GetToolResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := "needle-alpha\nsecond line"; got != want {
		t.Fatalf("GetToolResult() = %q, want %q", got, want)
	}

	second, err := s.SaveToolResult("bad/tool", "another body")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second, "0002-tool-") {
		t.Fatalf("second id = %q, want sequence 0002 and fallback tool name", second)
	}

	id, err = s.SaveToolResult("read", "")
	if err != nil || id != "" {
		t.Fatalf("empty save id=%q err=%v", id, err)
	}
}

func TestGetToolResultRejectsInvalidID(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	tests := []string{
		"../../etc/passwd",
		"/etc/passwd",
		`C:\\Windows\\system.ini`,
		"0001-read-deadbeef.txt/../secret",
		"0001-READ-deadbeef.txt",
		"0001-read-deadbee!.txt",
		"0001-weird.name-deadbeef.txt",
		"1-read-deadbeef.txt",
		"",
	}
	for _, id := range tests {
		t.Run(id, func(t *testing.T) {
			if _, err := s.GetToolResult(id); err == nil {
				t.Fatalf("GetToolResult(%q) succeeded", id)
			}
		})
	}
}

func TestGetToolResultMissing(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	_, err := s.GetToolResult("0001-read-deadbeef.txt")
	if err == nil || !strings.Contains(err.Error(), "tool result expired or missing") {
		t.Fatalf("err=%v, want explicit missing error", err)
	}
}

func TestToolResultPrunesFileCount(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	for i := 0; i < toolResultKeepFiles+5; i++ {
		if _, err := s.SaveToolResult("read", fmt.Sprintf("body-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := s.ToolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != toolResultKeepFiles {
		t.Fatalf("len(files)=%d, want %d", len(files), toolResultKeepFiles)
	}
	if base := filepath.Base(files[0]); !strings.HasPrefix(base, "0069-") {
		t.Fatalf("newest file = %q, want sequence 0069", base)
	}
	if base := filepath.Base(files[len(files)-1]); !strings.HasPrefix(base, "0006-") {
		t.Fatalf("oldest retained file = %q, want sequence 0006", base)
	}
}

func TestToolResultPrunesTotalBytes(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	// 5 × 7 MiB = 35 MiB > 32 MiB dir cap; each body fits the 8 MiB per-file
	// cap, so only total-bytes pruning applies.
	body := strings.Repeat("x", 7<<20)
	for i := 0; i < 5; i++ {
		if _, err := s.SaveToolResult("bash", fmt.Sprintf("%d%s", i, body)); err != nil {
			t.Fatal(err)
		}
	}
	bytes, err := s.ToolBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes > toolResultMaxDirBytes {
		t.Fatalf("ToolBytes()=%d, exceeds cap %d", bytes, toolResultMaxDirBytes)
	}
	files, err := s.ToolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("len(files)=%d, want 4 after byte pruning", len(files))
	}
}

func TestToolResultPermissions(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	id, err := s.SaveToolResult("read", "secret")
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(s.ToolDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("tool dir permissions=%#o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(s.ToolDir(), id))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("tool file permissions=%#o, want 0600", got)
	}
}

func TestToolFilesMissingDir(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	files, err := s.ToolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("ToolFiles()=%v, want empty", files)
	}
}

func TestToolDirGuard(t *testing.T) {
	tests := []*Store{nil, {}, {Dir: t.TempDir()}, {Dir: t.TempDir(), ID: ".."}}
	for _, s := range tests {
		if got := s.ToolDir(); got != "" {
			t.Fatalf("ToolDir()=%q, want empty", got)
		}
	}
}

func TestSaveToolResultRejectsOversized(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "20260101T000000"}
	huge := strings.Repeat("x", toolResultMaxReadBytes+1)
	if _, err := s.SaveToolResult("read", huge); err == nil {
		t.Fatal("oversized body must be rejected (a stored id must stay retrievable)")
	}
	if _, err := s.GetToolResult("0001-read-aabbccdd.txt"); err == nil || !strings.Contains(err.Error(), "expired or missing") {
		t.Fatalf("oversized save must not leave a file behind, got %v", err)
	}
}

// TestSaveToolResultConcurrent exercises the per-store mutex: parallel saves
// must yield unique sequences, all retrievable, with no prune race. Run under
// -race.
func TestSaveToolResultConcurrent(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "20260101T000000"}
	const n = 24
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = s.SaveToolResult("bash", fmt.Sprintf("body-%d-%s", i, strings.Repeat("z", 200)))
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("save %d: %v", i, errs[i])
		}
		if seen[ids[i]] {
			t.Fatalf("duplicate id %q (sequence collision)", ids[i])
		}
		seen[ids[i]] = true
		got, err := s.GetToolResult(ids[i])
		if err != nil {
			t.Fatalf("retrieve %d (%s): %v", i, ids[i], err)
		}
		if !strings.Contains(got, fmt.Sprintf("body-%d-", i)) {
			t.Fatalf("save %d: wrong body %q", i, got[:40])
		}
	}
}

func TestSaveToolResultRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: root, ID: "session"}
	if err := os.Symlink(target, s.ToolDir()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.SaveToolResult("read", "secret"); err == nil {
		t.Fatal("SaveToolResult accepted a symlinked tool directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote through symlink: %v", entries)
	}
}

func TestSaveToolResultRotatesAtSequenceLimit(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "session"}
	if err := os.MkdirAll(s.ToolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"9999-read-aabbccdd.txt",
		"10000-read-11223344.txt", // artifact from versions before rotation
	} {
		if err := os.WriteFile(filepath.Join(s.ToolDir(), name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.SaveToolResult("read", "new body")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "0001-read-") {
		t.Fatalf("id = %q, want rotated 0001 sequence", id)
	}
	files, err := os.ReadDir(s.ToolDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name() != id {
		t.Fatalf("files after rotation = %v, want only %q", files, id)
	}
	if got, err := s.GetToolResult(id); err != nil || got != "new body" {
		t.Fatalf("GetToolResult(%q) = %q, %v", id, got, err)
	}
}
