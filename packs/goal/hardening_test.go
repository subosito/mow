package goal

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreConcurrentSave(t *testing.T) {
	dir := t.TempDir()
	// Value copies that share Dir must still serialize (dir-keyed locks).
	s1 := Store{Dir: dir}
	s2 := Store{Dir: dir}
	st := State{ID: "race", Goal: "g", Status: StatusPending, MaxSteps: 3}
	const n = 16
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			copy := st
			copy.Summary = strings.Repeat("x", i%100)
			if i%2 == 0 {
				errCh <- s1.Save(copy)
			} else {
				errCh <- s2.Save(copy)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s1.Load("race")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "race" {
		t.Fatalf("load=%+v", got)
	}
}

func TestSanitizeStateStatusInvariant(t *testing.T) {
	st := State{Status: Status("not-a-status"), Step: -3, MaxSteps: 999}
	sanitizeState(&st)
	if st.Status != StatusPending {
		t.Fatalf("status=%q", st.Status)
	}
	if st.Step != 0 || st.MaxSteps != MaxMaxSteps {
		t.Fatalf("step=%d max=%d", st.Step, st.MaxSteps)
	}
}

func TestStoreRejectsSymlinkGoalFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "goals")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"id":"evil","goal":"leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "evil.json")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	s := &Store{Dir: dir}
	if _, err := s.Load("evil"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("want regular-file error, got %v", err)
	}
}

func TestStoreRejectsSymlinkDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "goals")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	s := &Store{Dir: link}
	if err := s.Save(State{ID: "x", Goal: "g", MaxSteps: 1}); err == nil || !strings.Contains(err.Error(), "regular directory") {
		t.Fatalf("want dir error, got %v", err)
	}
}

func TestAcquireRunExclusive(t *testing.T) {
	release, err := acquireRun("solo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRun("solo"); err == nil {
		t.Fatal("expected duplicate run error")
	}
	release()
	release2, err := acquireRun("solo")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release2()
	// Idempotent double-release must not leave the id stuck or panic.
	release()
}

func TestSanitizeStateBounds(t *testing.T) {
	st := State{
		Goal:    strings.Repeat("g", maxStateTextRunes+100),
		Summary: strings.Repeat("s", maxStateTextRunes+100),
		Facts:   make([]Fact, maxFacts+5),
		Plan:    Plan{Items: make([]PlanItem, maxPlanItems+5)},
	}
	sanitizeState(&st)
	if len([]rune(st.Goal)) > maxStateTextRunes+1 {
		t.Fatalf("goal len=%d", len([]rune(st.Goal)))
	}
	if len(st.Facts) != maxFacts {
		t.Fatalf("facts=%d", len(st.Facts))
	}
	if len(st.Plan.Items) != maxPlanItems {
		t.Fatalf("plan items=%d", len(st.Plan.Items))
	}
}

func TestRedactGitSecrets(t *testing.T) {
	in := "fatal: https://user:secret-token@github.com/org/repo.git"
	out := redactSecrets(in)
	if strings.Contains(out, "secret-token") {
		t.Fatalf("leaked: %q", out)
	}
}
