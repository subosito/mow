package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/llm"
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

// Compare actual 200-turn replay records with legacy full-history writes.
func BenchmarkSessionReplay(b *testing.B) {
	for _, incremental := range []bool{false, true} {
		name := "full"
		if incremental {
			name = "incremental"
		}
		b.Run(name, func(b *testing.B) {
			s := &Store{Dir: b.TempDir(), ID: "replay"}
			messages := []llm.Message{{Role: "system", Content: "system"}}
			for range 200 {
				messages = append(messages, llm.Message{Role: "user", Content: strings.Repeat("u", 1024)}, llm.Message{Role: "assistant", Content: strings.Repeat("a", 1024)})
				var err error
				if incremental {
					err = s.AppendSnapshot(messages)
				} else {
					err = s.Append(Event{Type: "snapshot", Messages: messages})
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			st, err := os.Stat(s.Path())
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				got, err := s.LoadMessages()
				if err != nil || len(got) != len(messages) {
					b.Fatalf("loaded %d messages, want %d: %v", len(got), len(messages), err)
				}
			}
			b.ReportMetric(float64(st.Size()), "session-bytes")
		})
	}
}

// BenchmarkSessionAppend: 200 events, one open+flock+write per call.
func BenchmarkSessionAppend(b *testing.B) {
	s := &Store{Dir: b.TempDir(), ID: "bench"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := s.Append(benchEvent(i % 200)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSessionLoadMessages: 200-turn session (200 user/assistant lines).
func BenchmarkSessionLoadMessages(b *testing.B) {
	s := &Store{Dir: b.TempDir(), ID: "bench"}
	for i := range 200 {
		if err := s.Append(benchEvent(i)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
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
	for i := range 1000 {
		s := &Store{Dir: dir, ID: string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))}
		if err := s.Append(benchEvent(i)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := LatestID(dir); err != nil {
			b.Fatal(err)
		}
	}
}
