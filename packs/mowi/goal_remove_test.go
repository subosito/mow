package mowi

import (
	"os"
	"testing"

	"github.com/subosito/mow/packs/goal"
)

func TestGoalRemoveDeletesStoredGoal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	store := &goal.Store{}
	if err := store.Save(goal.State{ID: "remove-me", Goal: "temporary", Status: goal.StatusPending}); err != nil {
		t.Fatal(err)
	}
	m := freshModel(t)
	_ = m.handleSlash("/goal remove remove-me")
	if _, err := store.Load("remove-me"); !os.IsNotExist(err) {
		t.Fatalf("goal still exists or unexpected error: %v", err)
	}
	if !lastEntryContains(m, "deleted remove-me") {
		t.Fatal("remove should report deleted goal")
	}
}

func TestGoalDeleteAliasAndNotFound(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	m := freshModel(t)
	_ = m.handleSlash("/goal delete missing")
	if !lastEntryContains(m, "not found") {
		t.Fatal("delete alias should report a missing goal")
	}
}

func TestGoalRemoveRefusesRunningGoal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	store := &goal.Store{}
	if err := store.Save(goal.State{ID: "active", Goal: "running", Status: goal.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	m := freshModel(t)
	_ = m.handleSlash("/goal rm active")
	if _, err := store.Load("active"); err != nil {
		t.Fatalf("running goal should remain: %v", err)
	}
	if !lastEntryContains(m, "is running") {
		t.Fatal("remove should explain running-goal refusal")
	}
}

func TestGoalForceRemoveDeletesUnownedRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	store := &goal.Store{}
	if err := store.Save(goal.State{ID: "stuck", Goal: "running", Status: goal.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	m := freshModel(t)
	_ = m.handleSlash("/goal remove stuck --force")
	if _, err := store.Load("stuck"); !os.IsNotExist(err) {
		t.Fatalf("force should delete leftover StatusRunning: %v", err)
	}
	if !lastEntryContains(m, "deleted stuck") {
		t.Fatal("force remove should report deleted goal")
	}
}
