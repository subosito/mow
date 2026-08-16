package job

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeStateID(t *testing.T) {
	if got := sanitizeStateID("ops-prod"); got != "ops-prod" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeStateID("a/b c"); !strings.Contains(got, "_") {
		t.Fatalf("expected sanitized id, got %q", got)
	}
	if got := sanitizeStateID(""); got != "_" {
		t.Fatalf("empty -> _, got %q", got)
	}
}

func TestTickStateRoundTrip(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	if st, err := LoadTick("solo"); err != nil || st.LastStatus != "" {
		t.Fatalf("missing state: %+v err=%v", st, err)
	}
	if err := SaveTick(TickState{ID: "solo", LastStatus: "ok", FireCount: 1, LastEnd: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadTick("solo")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "ok" || got.FireCount != 1 {
		t.Fatalf("got %+v", got)
	}
	if FormatTick(got) == "-" {
		t.Fatal("FormatTick should not be empty after a save")
	}
}

func TestRecordTickSkipAndEnd(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	st := recordTickSkip("j1", "previous tick still running")
	if st.SkipCount != 1 || st.SkipTotal != 1 || st.LastStatus != "skip" {
		t.Fatalf("first skip: %+v", st)
	}
	st = recordTickSkip("j1", "previous tick still running")
	if st.SkipCount != 2 || st.SkipTotal != 2 {
		t.Fatalf("second skip: %+v", st)
	}
	recordTickStart("j1")
	recordTickEnd("j1", "ok", "")
	got, err := LoadTick("j1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "ok" || got.SkipCount != 0 || got.SkipTotal != 2 || got.FireCount != 1 {
		t.Fatalf("after ok: %+v", got)
	}
}

func TestFormatTickEmpty(t *testing.T) {
	if got := FormatTick(TickState{}); got != "-" {
		t.Fatalf("got %q", got)
	}
}
