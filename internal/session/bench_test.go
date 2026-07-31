package session

import (
	"testing"
	"time"
)

// Baseline perf harness for session JSONL append/load — the persistence
// path of every turn.

func benchEvent(i int) Event {
	return Event{
		Type:    "message",
		TS:      time.Now().UTC(),
		Role:    "user",
		Content: "01234567890123456789012345678901234567890123456789 " + string(rune('a'+i%26)),
	}
}

// BenchmarkSessionAppend: 200 events, one open+flock+write per call.
func BenchmarkSessionAppend(b *testing.B) {
	s := &Store{Dir: b.TempDir(), ID: "bench"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Append(benchEvent(i % 200)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSessionLoadMessages: 200-turn session (200 user/assistant lines).
func BenchmarkSessionLoadMessages(b *testing.B) {
	s := &Store{Dir: b.TempDir(), ID: "bench"}
	for i := 0; i < 200; i++ {
		if err := s.Append(benchEvent(i)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgs, err := s.LoadMessages()
		if err != nil {
			b.Fatal(err)
		}
		if len(msgs) == 0 {
			b.Fatal("no messages loaded")
		}
	}
}

// BenchmarkSessionLatestID: list/scan across 1000 session files.
func BenchmarkSessionLatestID(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 1000; i++ {
		s := &Store{Dir: dir, ID: string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))}
		if err := s.Append(benchEvent(i)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LatestID(dir); err != nil {
			b.Fatal(err)
		}
	}
}
