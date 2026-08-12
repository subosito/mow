package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/goal"
)

func TestValidateJobDisabledIsErrDisabled(t *testing.T) {
	off := false
	err := ValidateJob(Job{ID: "x", Every: "1h", Prompt: "p", Enabled: &off})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("got %v, want ErrDisabled", err)
	}
	if err := ValidateJob(Job{ID: "x\ny", Every: "1h", Prompt: "p"}); err == nil {
		t.Fatal("want invalid id")
	}
}

func TestDuplicateScheduleIDs(t *testing.T) {
	err := duplicateScheduleIDs([]Job{
		{ID: "a", Every: "1h", Prompt: "p"},
		{ID: " a ", Every: "2h", Prompt: "q"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadSchedulesRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSchedules(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadSchedulesRejectsOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSchedulesFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSchedules(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadSchedulesCapsCount(t *testing.T) {
	var b strings.Builder
	b.WriteString("schedules:\n")
	for i := 0; i < maxSchedules+1; i++ {
		b.WriteString("  - id: j\n    every: 1h\n    prompt: p\n")
	}
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSchedules(path); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadSchedulesFollowsSymlinkToRegular(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(real, []byte("schedules:\n  - id: a\n    every: 1h\n    prompt: p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "schedules.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	jobs, err := LoadSchedules(link)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "a" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestLoadSchedulesForCLIExplicitMissing(t *testing.T) {
	_, _, err := loadSchedulesForCLI(filepath.Join(t.TempDir(), "nope.yaml"), nil)
	if err == nil || !strings.Contains(err.Error(), "no file") {
		t.Fatalf("got %v", err)
	}
}

func TestStartRejectsDuplicates(t *testing.T) {
	d := &Daemon{
		NewEngine: func() (*mow.Engine, error) { return nil, errors.New("unused") },
		Schedules: []Job{
			{ID: "a", Every: "1h", Prompt: "p"},
			{ID: "a", Every: "2h", Prompt: "q"},
		},
	}
	if err := d.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}

func TestEveryDoesNotFireWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var n atomic.Int32
	d := &Daemon{
		NewEngine: func() (*mow.Engine, error) {
			n.Add(1)
			return nil, errors.New("should not build engine")
		},
	}
	d.runEveryLoop(ctx, Job{ID: "t", Every: "1h", Prompt: "p"}, time.Hour)
	if n.Load() != 0 {
		t.Fatalf("cancelled ctx fired %d times", n.Load())
	}
}

func TestFireSkipsOverlap(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	var logs []string
	d := &Daemon{
		OnLog: func(msg string) { logs = append(logs, msg) },
		NewEngine: func() (*mow.Engine, error) {
			return mow.New(mow.Options{
				NoSession: true,
				Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
					select {
					case started <- struct{}{}:
					default:
					}
					<-block
					return mow.Message{Role: "assistant", Content: "ok"}, nil
				},
			})
		},
	}
	j := Job{ID: "solo", Prompt: "p"}
	done := make(chan struct{})
	go func() {
		d.fire(context.Background(), j)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first tick")
	}
	d.fire(context.Background(), j)
	close(block)
	<-done
	found := false
	for _, l := range logs {
		if strings.Contains(l, "skip: previous tick") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected overlap skip, logs=%v", logs)
	}
}

func TestFireResetRestoresPlan(t *testing.T) {
	store := &goal.Store{Dir: t.TempDir()}
	if err := store.Save(goal.State{
		ID: "goal-1", Goal: "do task", Status: goal.StatusDone,
		Plan: goal.Plan{Items: []goal.PlanItem{{ID: "a", Title: "work", Status: goal.ItemDone}}},
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		GoalStore: store,
		NewEngine: func() (*mow.Engine, error) {
			return mow.New(mow.Options{
				NoSession: true,
				Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
					return mow.Message{Role: "assistant", Content: "ok"}, nil
				},
			})
		},
		OnLog: func(string) {},
	}
	d.fire(context.Background(), Job{ID: "j", Goal: "goal-1"})
	got, err := store.Load("goal-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Plan.Items) != 1 || got.Plan.Items[0].Status != goal.ItemPending {
		t.Fatalf("plan after recurring reset: %+v", got.Plan.Items)
	}
}

func TestFireSkipsBlockedGoal(t *testing.T) {
	store := &goal.Store{Dir: t.TempDir()}
	if err := store.Save(goal.State{
		ID: "goal-b", Goal: "pick", Status: goal.StatusBlocked, Question: "which?",
	}); err != nil {
		t.Fatal(err)
	}
	var chats atomic.Int32
	var logs []string
	d := &Daemon{
		GoalStore: store,
		OnLog:     func(msg string) { logs = append(logs, msg) },
		NewEngine: func() (*mow.Engine, error) {
			return mow.New(mow.Options{
				NoSession: true,
				Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
					chats.Add(1)
					return mow.Message{Role: "assistant", Content: "should not run"}, nil
				},
			})
		},
	}
	d.fire(context.Background(), Job{ID: "j", Goal: "goal-b"})
	if chats.Load() != 0 {
		t.Fatal("blocked goal must not start a run")
	}
	got, err := store.Load("goal-b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != goal.StatusBlocked {
		t.Fatalf("status=%s", got.Status)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "blocked") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected blocked skip log, got %v", logs)
	}
}

func TestCmdCheckDisabledNotBad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	raw := []byte("schedules:\n  - id: off\n    every: 1h\n    prompt: p\n    enabled: false\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdCheck([]string{"--schedules", path}); code != 0 {
		t.Fatalf("disabled schedule should check ok, code=%d", code)
	}
}

func TestCmdRunExplicitMissingSchedules(t *testing.T) {
	if code := cmdRun([]string{"--schedules", filepath.Join(t.TempDir(), "nope.yaml")}); code != 1 {
		t.Fatalf("explicit missing --schedules should fail, code=%d", code)
	}
}

func TestCmdCheckDuplicateIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	raw := []byte("schedules:\n  - id: a\n    every: 1h\n    prompt: p\n  - id: a\n    every: 2h\n    prompt: q\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdCheck([]string{"--schedules", path}); code != 1 {
		t.Fatalf("duplicate ids should fail check, code=%d", code)
	}
}
