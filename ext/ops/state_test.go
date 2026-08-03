package ops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadStateMissingIsEmpty(t *testing.T) {
	t.Parallel()
	st, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || len(st.Services) != 0 || len(st.Signatures) != 0 {
		t.Fatalf("want empty state, got %+v", st)
	}
}

func TestLoadStateCorrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err == nil {
		t.Fatal("expected corrupt state error")
	}
}

func TestSaveLoadStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &TickState{}
	UpdateLogOffset(st, "gw", "/var/log/gw.log", 4096, 42)
	UpdateSignature(st, "sig-502", 3)
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	ls, ok := got.Services["gw"].Logs["/var/log/gw.log"]
	if !ok || ls.Offset != 4096 || ls.Inode != 42 {
		t.Fatalf("log state=%+v ok=%v", ls, ok)
	}
	if got.Signatures["sig-502"].Count != 3 {
		t.Fatalf("sig state=%+v", got.Signatures)
	}
}

func TestSaveStateNil(t *testing.T) {
	t.Parallel()
	if err := SaveState(t.TempDir(), nil); err == nil {
		t.Fatal("nil state must error")
	}
}

func TestSaveStateOverwrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &TickState{}
	UpdateSignature(st, "a", 1)
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	st2 := &TickState{}
	UpdateSignature(st2, "b", 2)
	if err := SaveState(dir, st2); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, hasA := got.Signatures["a"]; hasA {
		t.Fatal("stale entry survived overwrite")
	}
	if got.Signatures["b"].Count != 2 {
		t.Fatalf("got=%+v", got)
	}
}

func TestUpdateLogOffsetGuards(t *testing.T) {
	t.Parallel()
	UpdateLogOffset(nil, "svc", "/x", 1, 1) // no panic
	st := &TickState{}
	UpdateLogOffset(st, "", "/x", 1, 1)
	UpdateLogOffset(st, "svc", "", 1, 1)
	if len(st.Services) != 0 {
		t.Fatalf("empty keys must be ignored: %+v", st)
	}
	// overwrite same path
	UpdateLogOffset(st, "svc", "/x", 10, 1)
	UpdateLogOffset(st, "svc", "/x", 20, 2)
	if st.Services["svc"].Logs["/x"].Offset != 20 {
		t.Fatalf("offset not updated: %+v", st)
	}
}

func TestUpdateSignatureGuards(t *testing.T) {
	t.Parallel()
	UpdateSignature(nil, "sig", 1) // no panic
	st := &TickState{}
	UpdateSignature(st, "", 1)
	if len(st.Signatures) != 0 {
		t.Fatalf("empty sig must be ignored: %+v", st)
	}
	UpdateSignature(st, "sig", 5)
	UpdateSignature(st, "sig", 9)
	if st.Signatures["sig"].Count != 9 {
		t.Fatalf("count=%d", st.Signatures["sig"].Count)
	}
}

func TestShouldNotify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(*TickState)
		cooldown time.Duration
		want     bool
	}{
		{"nil state", nil, time.Minute, false},
		{"unknown sig", func(s *TickState) {}, time.Minute, false},
		{"zero count", func(s *TickState) { UpdateSignature(s, "sig", 0) }, time.Minute, false},
		{"count set, never notified", func(s *TickState) { UpdateSignature(s, "sig", 2) }, time.Minute, true},
		{"notified just now", func(s *TickState) {
			UpdateSignature(s, "sig", 2)
			MarkNotified(s, "sig")
		}, time.Hour, false},
		{"cooldown elapsed", func(s *TickState) {
			UpdateSignature(s, "sig", 2)
			MarkNotified(s, "sig")
		}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &TickState{}
			if c.mutate != nil {
				c.mutate(st)
			}
			if got := ShouldNotify(st, "sig", c.cooldown); got != c.want {
				t.Fatalf("ShouldNotify=%v want %v", got, c.want)
			}
		})
	}
}

func TestMarkNotifiedGuards(t *testing.T) {
	t.Parallel()
	MarkNotified(nil, "sig") // no panic
	st := &TickState{}
	MarkNotified(st, "")
	if len(st.Signatures) != 0 {
		t.Fatalf("empty sig must be ignored: %+v", st)
	}
	MarkNotified(st, "sig")
	if st.Signatures["sig"].LastNotified.IsZero() {
		t.Fatal("LastNotified must be stamped")
	}
}
