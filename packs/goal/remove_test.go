package goal

import (
	"errors"
	"os"
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
	// Force overrides the guard and deletes the file.
	if err := s.Remove("r1", true); err != nil {
		t.Fatalf("force remove running: %v", err)
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
	// Legacy Delete keeps "missing file is not an error" semantics.
	if err := s.Delete("missing"); err != nil {
		t.Fatalf("legacy delete missing: %v", err)
	}
	// Delete still works on a present file and is unguarded (deprecated path).
	if err := s.Save(State{ID: "d1", Status: StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("d1"); err != nil {
		t.Fatalf("legacy delete present: %v", err)
	}
}
