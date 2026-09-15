package permstore

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestRememberLookupRevoke(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	ws := filepath.Join(t.TempDir(), "proj")

	rule, err := Remember(ws, "bash", `{"command":"ls"}`, Allow)
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" || rule.Tool != "bash" {
		t.Fatalf("rule: %+v", rule)
	}
	d, ok, err := Lookup(ws, "bash", `{"command":"ls"}`)
	if err != nil || !ok || d != Allow {
		t.Fatalf("lookup allow: d=%q ok=%v err=%v", d, ok, err)
	}
	if _, ok, _ := Lookup(ws, "bash", `{"command":"rm -rf /"}`); ok {
		t.Fatal("different args must not match")
	}

	if _, err := Remember(ws, "bash", `{"command":"ls"}`, Deny); err != nil {
		t.Fatal(err)
	}
	d, ok, err = Lookup(ws, "bash", `{"command":"ls"}`)
	if err != nil || !ok || d != Deny {
		t.Fatalf("lookup deny: d=%q ok=%v err=%v", d, ok, err)
	}

	ok, err = Revoke(ws, rule.ID)
	if err != nil || !ok {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	if _, ok, _ = Lookup(ws, "bash", `{"command":"ls"}`); ok {
		t.Fatal("revoked rule still matches")
	}
	rules, err := Load(ws)
	if err != nil || len(rules) != 0 {
		t.Fatalf("load after revoke: %+v err=%v", rules, err)
	}
}

// TestConcurrentRememberKeepsEveryRule: concurrent writers must not lose a
// rule. The package mutex covers goroutines; the file lock plus a per-writer
// temp cover the cross-process case a shared path+".tmp" got wrong.
func TestConcurrentRememberKeepsEveryRule(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	ws := filepath.Join(t.TempDir(), "proj")

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			_, errs[i] = Remember(ws, "bash", fmt.Sprintf(`{"command":"cmd-%d"}`, i), Allow)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	rules, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != n {
		t.Fatalf("got %d rules, want %d", len(rules), n)
	}
	for i := range n {
		if _, ok, _ := Lookup(ws, "bash", fmt.Sprintf(`{"command":"cmd-%d"}`, i)); !ok {
			t.Fatalf("rule %d lost", i)
		}
	}
}

// TestFileLockExcludes: a second acquirer waits for the first to release.
func TestFileLockExcludes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no flock")
	}
	path := filepath.Join(t.TempDir(), "rules.json.lock")
	first, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil {
		t.Skip("file locking unavailable on this platform")
	}
	got := make(chan struct{})
	go func() {
		second, err := lockFile(path)
		if err == nil {
			unlockFile(second)
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock acquired while the first was held")
	case <-time.After(50 * time.Millisecond):
	}
	unlockFile(first)
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after release")
	}
}
