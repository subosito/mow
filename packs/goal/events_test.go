package goal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AppendEvent is a public method on an exported Store, and it is documented as
// best-effort — which means a bad id fails silently. That combination makes an
// unvalidated id worse than elsewhere, not better: nothing would surface the
// escape. Reported by `mow sec` reviewing this repo.
func TestAppendEventRejectsTraversalID(t *testing.T) {
	root := t.TempDir()
	s := &Store{Dir: filepath.Join(root, "goals")}

	// A canary outside the goals dir that a traversal id would target.
	outside := filepath.Join(root, "escaped")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{
		"../escaped",
		"../../escaped",
		"a/../../escaped",
		"/etc/cron.d/x",
		"..",
		".",
		"",
		"   ",
		"has space",
		"semi;colon",
	} {
		s.AppendEvent(id, LogEvent{Kind: "start", Text: "should not be written"})
	}

	// Nothing may have been written outside the goals directory.
	var strays []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(p, filepath.Join(root, "goals")+string(filepath.Separator)) {
			strays = append(strays, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(strays) > 0 {
		t.Fatalf("AppendEvent wrote outside the goals dir: %v", strays)
	}
}

// The happy path must still work: validation should reject traversal, not
// ordinary slugs.
func TestAppendEventWritesValidID(t *testing.T) {
	root := t.TempDir()
	s := &Store{Dir: filepath.Join(root, "goals")}
	s.AppendEvent("fix-ci", LogEvent{Kind: "start", Text: "hello"})

	path := filepath.Join(root, "goals", "fix-ci", "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("valid id should still append: %v", err)
	}
	if !strings.Contains(string(raw), "hello") {
		t.Fatalf("event body missing: %s", raw)
	}
}
