package job

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

func TestCronNextAfterDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// Instants are fixed in UTC so ambiguous/nonexistent wall times never
	// depend on time.Date's DST disambiguation; after is converted to ny so
	// the schedule is evaluated on New York wall clocks.
	cases := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{
			// Spring forward 2026-03-08: 02:00 EST jumps to 03:00 EDT, so
			// 02:30 never exists; vixie fires once right after the gap.
			name:  "spring forward fires at transition",
			expr:  "30 2 * * *",
			after: time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), // 01:00 EST
			want:  time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), // 03:00 EDT
		},
		{
			name:  "normal day still 02:30",
			expr:  "30 2 * * *",
			after: time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC),  // 01:00 EDT
			want:  time.Date(2026, 6, 15, 6, 30, 0, 0, time.UTC), // 02:30 EDT
		},
		{
			// Fall back 2026-11-01: 02:00 EDT rewinds to 01:00 EST, so
			// 01:30 occurs twice; the first (EDT) occurrence fires.
			name:  "fall back fires first occurrence",
			expr:  "30 1 * * *",
			after: time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),  // 00:00 EDT
			want:  time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), // 01:30 EDT
		},
		{
			// After the first 01:30, the repeated (EST) 01:30 must be
			// suppressed: next fire is the following day.
			name:  "fall back does not fire twice",
			expr:  "30 1 * * *",
			after: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), // 01:30 EDT (first pass)
			want:  time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC), // Nov 2 01:30 EST
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseCron(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.nextAfter(tc.after.In(ny))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("nextAfter(%v) = %v, want %v", tc.after.In(ny), got, tc.want.In(ny))
			}
		})
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
