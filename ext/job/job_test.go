package job

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSchedules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.yaml")
	raw := []byte("schedules:\n  - id: a\n    every: 1h\n    goal: g1\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	jobs, err := LoadSchedules(path)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "a" {
		t.Fatalf("%+v %v", jobs, err)
	}
}
