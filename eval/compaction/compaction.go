// Package compaction measures whether the opt-in compaction summarizer
// (policy.compact_summary) actually saves money.
//
// The premise behind the feature is that a deterministic compaction stub makes
// the model lose the thread and re-explore — re-reading files it already read,
// re-deriving conclusions it already reached — and that one summary call costs
// less than that re-exploration. That is a plausible claim, not a proven one,
// and it is easy to get backwards: the summary call is a certain cost paid at
// every compaction, while the saving is probabilistic.
//
// This harness makes the comparison concrete. It replays the same scripted
// session with the summarizer off and on, and reports total input tokens, the
// summary spend, and post-compaction tool repetition.
//
// Run with a live model endpoint:
//
//	go test ./eval/compaction -run TestCompactionCost -v
//
// Without credentials the test skips: the whole point is real token counts.
package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/subosito/mow"
)

// Report is the per-arm measurement.
type Report struct {
	// Arm is "stub" or "summary".
	Arm string
	// InputTokens is the sum of provider-reported input tokens across all
	// requests in the run. This is the number that shows up on the bill.
	InputTokens int
	// OutputTokens is the sum of provider-reported output tokens.
	OutputTokens int
	// SummaryInputTokens / SummaryOutputTokens are the portion spent by the
	// compaction summarizer itself — the cost side of the trade.
	SummaryInputTokens  int
	SummaryOutputTokens int
	// Compactions is how many times history was compacted.
	Compactions int
	// RepeatedReads counts reads of a path that was already read earlier in
	// the same run. This is the re-exploration the summary is supposed to
	// prevent, and the clearest behavioral signal available.
	RepeatedReads int
	// Tools is the total number of tool calls.
	Tools int
}

// Net returns the input-token difference against a baseline arm. Negative
// means this arm was cheaper.
func (r Report) Net(baseline Report) int {
	return r.InputTokens - baseline.InputTokens
}

func (r Report) String() string {
	return fmt.Sprintf(
		"%-8s input=%-8d output=%-7d summary(in/out)=%d/%d compactions=%d tools=%d repeated_reads=%d",
		r.Arm, r.InputTokens, r.OutputTokens,
		r.SummaryInputTokens, r.SummaryOutputTokens,
		r.Compactions, r.Tools, r.RepeatedReads)
}

// Options configures one arm.
type Options struct {
	Workspace string
	// Prompts are fed in order to the same Engine, accumulating history so
	// compaction actually triggers. A single prompt will not exercise this.
	Prompts []string
	// MaxContextChars forces compaction early enough to observe. Real budgets
	// are far larger; a small value here keeps the eval affordable.
	MaxContextChars int
	// Summary enables policy.compact_summary for this arm.
	Summary bool
}

// Run executes one arm and returns its measurement.
func Run(ctx context.Context, opt Options) (Report, error) {
	rep := Report{Arm: "stub"}
	if opt.Summary {
		rep.Arm = "summary"
	}

	var mu sync.Mutex
	readPaths := map[string]int{}

	eopt := mow.Options{
		Workspace:       opt.Workspace,
		MaxContextChars: opt.MaxContextChars,
		CompactSummary:  opt.Summary,
	}
	eopt.OnEvent = func(ev mow.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case mow.EventRunEnd:
			rep.InputTokens += ev.InputTokens
			rep.OutputTokens += ev.OutputTokens
		case mow.EventCompactSummary:
			rep.SummaryInputTokens += ev.InputTokens
			rep.SummaryOutputTokens += ev.OutputTokens
		case mow.EventCompact:
			rep.Compactions++
		case mow.EventToolEnd:
			rep.Tools++
			if p := readArgPath(ev); p != "" {
				readPaths[p]++
			}
		}
	}

	eng, err := mow.New(eopt)
	if err != nil {
		return rep, err
	}
	defer eng.Close()

	for _, p := range opt.Prompts {
		if _, err := eng.Prompt(ctx, p); err != nil {
			return rep, fmt.Errorf("prompt %q: %w", truncate(p, 40), err)
		}
	}

	mu.Lock()
	for _, n := range readPaths {
		if n > 1 {
			// Every read past the first is a re-read.
			rep.RepeatedReads += n - 1
		}
	}
	mu.Unlock()
	return rep, nil
}

// Compare runs both arms against the same prompts and returns a verdict line.
//
// The arms are NOT independent samples of the same distribution — model
// sampling makes each run noisy — so a single Compare is indicative, not
// conclusive. Run it several times before believing a small difference.
func Compare(ctx context.Context, opt Options) (stub, summary Report, verdict string, err error) {
	o := opt
	o.Summary = false
	if stub, err = Run(ctx, o); err != nil {
		return
	}
	o.Summary = true
	if summary, err = Run(ctx, o); err != nil {
		return
	}
	delta := summary.Net(stub)
	switch {
	case stub.Compactions == 0 && summary.Compactions == 0:
		verdict = "inconclusive: no compaction occurred — lower MaxContextChars or add prompts"
	case delta < 0:
		verdict = fmt.Sprintf("summary cheaper by %d input tokens (spent %d on summaries)",
			-delta, summary.SummaryInputTokens)
	default:
		verdict = fmt.Sprintf("summary costlier by %d input tokens (spent %d on summaries)",
			delta, summary.SummaryInputTokens)
	}
	return
}

// readArgPath extracts a file path from a read-like tool event.
func readArgPath(ev mow.Event) string {
	if ev.Tool != "read" {
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(ev.Args, &a); err != nil {
		return ""
	}
	return a.Path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
