package goal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRemoveGuardsRunning(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("r1", false); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("remove running: got %v, want ErrGoalRunning", err)
	}
	// Still present — refused.
	if _, err := s.Load("r1"); err != nil {
		t.Fatalf("running goal should still exist after refused remove: %v", err)
	}
}

func TestStoreForceRemoveBypassesUnownedRunning(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	// No live run lock: --force deletes leftover StatusRunning.
	if err := s.Remove("r1", true); err != nil {
		t.Fatalf("force remove unowned running: %v", err)
	}
	if _, err := s.Load("r1"); !os.IsNotExist(err) {
		t.Fatalf("load after force remove: got %v, want not-exist", err)
	}
}

func TestStoreRemoveAllowsBlocked(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "b1", Status: StatusBlocked, Goal: "g", Question: "pick one"}); err != nil {
		t.Fatal(err)
	}
	// Blocked is not actively executing: allow deletion without force.
	if err := s.Remove("b1", false); err != nil {
		t.Fatalf("remove blocked without force: %v", err)
	}
	if _, err := s.Load("b1"); !os.IsNotExist(err) {
		t.Fatalf("blocked goal should be gone: %v", err)
	}
}

func TestStoreRemoveNotFound(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Remove("missing", false); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("remove missing: got %v, want ErrGoalNotFound", err)
	}
	// Force still reports not-found (no running guard, but file is absent).
	if err := s.Remove("missing", true); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("force remove missing: got %v, want ErrGoalNotFound", err)
	}
}

func TestStoreResetRefusesRunning(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusRunning, Goal: "g", MaxSteps: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset("r1"); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("reset running: got %v, want ErrGoalRunning", err)
	}
	got, err := s.Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("reset must leave running goal intact, status=%s", got.Status)
	}
	// Healed (dead owner) Running becomes Pending on Load, then Reset works.
	mu := s.lock()
	mu.Lock()
	if err := s.writeGoalJSON(s.path("h1"), State{
		ID: "h1", Goal: "g", Status: StatusRunning, MaxSteps: 2,
		RunOwnerPID: 99999999,
	}); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()
	if _, err := s.Reset("h1"); err != nil {
		t.Fatalf("reset after heal: %v", err)
	}
}

func TestStoreDeleteLegacyNoErrorOnMissing(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Delete("missing"); err != nil {
		t.Fatalf("legacy delete missing: %v", err)
	}
	if _, err := os.Lstat(s.runLockPath("missing")); !os.IsNotExist(err) {
		t.Fatalf("missing delete must not create run.lock: %v", err)
	}
}

func TestStoreDeleteBypassesUnownedRunning(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "d1", Status: StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("d1"); err != nil {
		t.Fatalf("legacy delete unowned running: %v", err)
	}
}

func TestStoreDeleteRefusesHeldRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "d1", Status: StatusPending, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunExclusive(s, "d1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := s.Delete("d1"); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("delete while run lock held: got %v, want ErrGoalRunning", err)
	}
	if _, err := s.Load("d1"); err != nil {
		t.Fatalf("goal should still exist: %v", err)
	}
}

func TestStoreRemoveRefusesHeldRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusPending, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunExclusive(s, "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := s.Remove("r1", false); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("remove while run lock held: got %v, want ErrGoalRunning", err)
	}
	if _, err := s.Load("r1"); err != nil {
		t.Fatalf("goal should still exist: %v", err)
	}
}

func TestStoreResetRefusesHeldRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusPending, Goal: "g", MaxSteps: 2}); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunExclusive(s, "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := s.Reset("r1"); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("reset while run lock held: got %v, want ErrGoalRunning", err)
	}
}

func TestStoreRemoveCleansEvents(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "e1", Status: StatusPending, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	s.AppendEvent("e1", LogEvent{Kind: "start", Text: "hi"})
	eventsDir := filepath.Join(s.dir(), "e1")
	if _, err := os.Stat(filepath.Join(eventsDir, "events.jsonl")); err != nil {
		t.Fatalf("events file missing before remove: %v", err)
	}
	if err := s.Remove("e1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(eventsDir); !os.IsNotExist(err) {
		t.Fatalf("events dir still present after remove: %v", err)
	}
}

func TestStoreRemoveMissingDoesNotCreateRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, force := range []bool{false, true} {
		if err := s.Remove("missing", force); !errors.Is(err, ErrGoalNotFound) {
			t.Fatalf("remove missing force=%v: got %v, want ErrGoalNotFound", force, err)
		}
		if _, err := os.Lstat(s.runLockPath("missing")); !os.IsNotExist(err) {
			t.Fatalf("missing remove force=%v must not create run.lock: %v", force, err)
		}
	}
}

func TestStoreRemoveLeavesRunLockAcquirable(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusPending, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("r1", false); err != nil {
		t.Fatal(err)
	}
	// Advisory flock inodes are left in place on Release. A leftover must be
	// unlocked so a later Run can take it — never unlinked while a peer could
	// still hold the inode.
	fl, err := acquireFileLock(s.runLockPath("r1"), false)
	if err != nil {
		t.Fatalf("leftover run.lock must be acquirable: %v", err)
	}
	fl.Release()
}

func TestStoreResetMissingDoesNotCreateRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if _, err := s.Reset("missing"); !os.IsNotExist(err) {
		t.Fatalf("reset missing: got %v, want not-exist", err)
	}
	if _, err := os.Lstat(s.runLockPath("missing")); !os.IsNotExist(err) {
		t.Fatalf("missing reset must not create run.lock: %v", err)
	}
}

func TestStoreForceRemoveRefusesHeldRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusPending, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunExclusive(s, "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := s.Remove("r1", true); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("force remove while run lock held: got %v, want ErrGoalRunning", err)
	}
	if _, err := s.Load("r1"); err != nil {
		t.Fatalf("goal should still exist: %v", err)
	}
}

func TestStoreRelativeDir(t *testing.T) {
	root := t.TempDir()
	goals := filepath.Join(root, "goals")
	if err := os.MkdirAll(goals, 0o700); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, goals)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: rel}
	if err := s.Save(State{ID: "a", Goal: "g", Status: StatusPending}); err != nil {
		t.Fatalf("save with relative Dir: %v", err)
	}
	if _, err := s.Load("a"); err != nil {
		t.Fatalf("load with relative Dir: %v", err)
	}
}
