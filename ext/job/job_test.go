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

func TestValidateJob(t *testing.T) {
	if err := ValidateJob(Job{ID: "a", Every: "1h", Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJob(Job{ID: "a", Every: "nope", Goal: "g"}); err == nil {
		t.Fatal("want bad every")
	}
	if err := ValidateJob(Job{ID: "a", Cron: "0 9 * * *", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
}

func TestInlineJob(t *testing.T) {
	j, err := InlineJob("", "10m", "", "", "ping the health endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != "inline" || j.Every != "10m" || j.Prompt == "" {
		t.Fatalf("%+v", j)
	}
	j, err = InlineJob("ci", "1h", "", "fix-ci", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != "ci" || j.Goal != "fix-ci" {
		t.Fatalf("%+v", j)
	}
	if _, err := InlineJob("", "10m", "", "", ""); err == nil {
		t.Fatal("want error without goal/prompt")
	}
	if _, err := InlineJob("", "", "", "", "hi"); err == nil {
		t.Fatal("want error without every/cron")
	}
	if _, err := InlineJob("", "nope", "", "", "hi"); err == nil {
		t.Fatal("want bad every")
	}
	j, err = InlineJob("morn", "", "0 9 * * 1-5", "", "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Cron != "0 9 * * 1-5" {
		t.Fatalf("%+v", j)
	}
}
