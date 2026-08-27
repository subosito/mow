package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/packs/job"
)

func TestCmdCheckACPWithoutWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "acpprof")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "services:\n  - name: api\n    logs: [/tmp/api.log]\n    actions:\n      status: [true]\nacp:\n  peers:\n    - name: coder\n      command: [echo]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdCheck("acpprof", nil); code != 1 {
		t.Fatalf("expected check fail without workspace, got %d", code)
	}
}

func TestNoteOverlapIncident(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "fleet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := Profile{Name: "fleet", Dir: dir}
	noteOverlapIncident(p, "fleet", "job ops-fleet skip: previous tick still running (consecutive=1 total=1)")
	if out, err := listIncidents(p.incidentsDir()); err != nil || !strings.Contains(out, "(none)") {
		t.Fatalf("threshold 1 should not open: %q err=%v", out, err)
	}
	if err := job.SaveTick(job.TickState{ID: "ops-fleet", LastStatus: "skip", SkipCount: 2, SkipTotal: 2}); err != nil {
		t.Fatal(err)
	}
	noteOverlapIncident(p, "fleet", "job ops-fleet skip: previous tick still running (consecutive=2 total=2)")
	out, err := listIncidents(p.incidentsDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "job-overlap") && !strings.Contains(out, "skipped") {
		t.Fatalf("expected overlap incident, got %s", out)
	}
}
