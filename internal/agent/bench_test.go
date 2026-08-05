package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// Baseline perf harness for context compaction + ratio calibration —
// the per-call budget path in the agent loop.

func benchMessages(totalChars int) []llm.Message {
	var msgs []llm.Message
	per := 400 // ~400 chars per user/assistant pair
	for totalChars > 0 {
		body := strings.Repeat("x", min(per, totalChars))
		msgs = append(msgs,
			llm.Message{Role: "user", Content: body},
			llm.Message{Role: "assistant", Content: body},
		)
		totalChars -= per
	}
	return msgs
}

// BenchmarkCompactOptsLarge: 100k chars of history compacted to 50k.
func BenchmarkCompactOptsLarge(b *testing.B) {
	msgs := benchMessages(100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := CompactOpts(msgs, 50_000, "", 24_000)
		if len(out) == 0 {
			b.Fatal("empty compact result")
		}
	}
}

// BenchmarkCompactOptsUnderBudget: history below budget — should be a
// fast pass-through, no trimming work.
func BenchmarkCompactOptsUnderBudget(b *testing.B) {
	msgs := benchMessages(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CompactOpts(msgs, 100_000, "", 24_000)
	}
}

// BenchmarkRatioCalibrator: steady-state Observe+Ratio churn per call.
func BenchmarkRatioCalibrator(b *testing.B) {
	c := newRatioCalibrator()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Observe(1000, 250)
		_ = c.Ratio()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = fmt.Sprintf // keep fmt import stable

func BenchmarkApplyCompactUnderBudget(b *testing.B) {
	msgs := []llm.Message{{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("question ", 100)},
		{Role: "assistant", Content: strings.Repeat("answer ", 100)}}
	opt := Options{MaxContextChars: 100_000}
	calib := newRatioCalibrator()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := applyCompact(context.Background(), msgs, opt, calib); err != nil {
			b.Fatal(err)
		}
	}
}
