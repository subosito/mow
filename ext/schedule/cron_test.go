package schedule

import (
	"testing"
	"time"
)

func TestParseCronAndMatch(t *testing.T) {
	// every day at 09:30
	s, err := parseCron("30 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// construct local 09:30
	now := time.Now().Local()
	at := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, time.Local)
	if !s.match(at) {
		t.Fatalf("should match 09:30")
	}
	at2 := time.Date(now.Year(), now.Month(), now.Day(), 9, 31, 0, 0, time.Local)
	if s.match(at2) {
		t.Fatal("should not match 09:31")
	}
}

func TestCronNextAfter(t *testing.T) {
	s, err := parseCron("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 12, 2, 0, 0, time.Local)
	next, err := s.nextAfter(from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Minute()%5 != 0 {
		t.Fatalf("next=%v", next)
	}
	if !next.After(from) {
		t.Fatal("next must be after from")
	}
}

func TestParseCronListsAndRanges(t *testing.T) {
	s, err := parseCron("0 9-17 * * 1,3,5")
	if err != nil {
		t.Fatal(err)
	}
	// Monday 10:00
	mon := time.Date(2026, 1, 5, 10, 0, 0, 0, time.Local) // 2026-01-05 is Monday
	if mon.Weekday() != time.Monday {
		t.Fatalf("fixture weekday %v", mon.Weekday())
	}
	if !s.match(mon) {
		t.Fatal("expected mon 10:00 match")
	}
}
