package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/subosito/mow/internal/llm"
)

// Exploration thrash guards.
//
// Real coding goals need many read/grep/bash turns before the first write.
// Killing after N explore-only turns aborts legitimate work. We instead stop
// when exploration is unproductive: re-reading the same paths and replaying
// the same glob/grep/bash, with a high hard ceiling as a last resort.
const (
	// unproductiveStopAfter consecutive explore turns that add no new path /
	// query / command → ErrStuck (true thrash).
	unproductiveStopAfter = 10
	// exploreHardCap total consecutive explore-only turns (even if productive)
	// → ErrStuck. Backstop when MaxTurns is unlimited.
	exploreHardCap = 80
	// exploreWarnEvery injects a wrap-up nudge every N explore-only turns.
	exploreWarnEvery = 15
	// rereadLimit: after this many successful reads of the same path in one
	// Prompt, further reads return a short stub instead of the full file.
	rereadLimit = 1
)

// thrashState tracks per-Prompt exploration abuse.
type thrashState struct {
	mu sync.Mutex
	// path → times successfully read this Prompt
	reads map[string]int
	// exact tool name+args → times (glob/bash/grep)
	calls map[string]int
	// consecutive turns whose tools were all explore-only
	exploreStreak int
	// consecutive explore turns that introduced nothing new
	unproductive int
}

func newThrashState() *thrashState {
	return &thrashState{
		reads: make(map[string]int),
		calls: make(map[string]int),
	}
}

func isExploreTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "glob", "grep", "bash":
		return true
	default:
		return false
	}
}

func batchExploreOnly(calls []llm.ToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, tc := range calls {
		if !isExploreTool(tc.Function.Name) {
			return false
		}
	}
	return true
}

// noteTurn updates explore/unproductive streaks. Productive means at least one
// call targets a path/query/command not seen earlier this Prompt.
// Returns (warn, stop).
func (s *thrashState) noteTurn(calls []llm.ToolCall) (warn, stop bool) {
	if s == nil {
		return false, false
	}
	if !batchExploreOnly(calls) {
		s.exploreStreak = 0
		s.unproductive = 0
		return false, false
	}
	s.exploreStreak++
	if s.turnProductive(calls) {
		s.unproductive = 0
	} else {
		s.unproductive++
	}
	if s.unproductive >= unproductiveStopAfter {
		return false, true
	}
	if s.exploreStreak >= exploreHardCap {
		return false, true
	}
	if s.exploreStreak%exploreWarnEvery == 0 {
		return true, false
	}
	return false, false
}

// turnProductive reports whether any call in the batch is new this Prompt.
// Must run before tools execute (reads/calls maps hold prior turns only).
func (s *thrashState) turnProductive(calls []llm.ToolCall) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tc := range calls {
		name := strings.ToLower(strings.TrimSpace(tc.Function.Name))
		args := json.RawMessage(tc.Function.Arguments)
		switch name {
		case "read":
			path := strings.TrimSpace(toolArgString(args, "path"))
			if path == "" {
				continue
			}
			if _, seen := s.reads[path]; !seen {
				return true
			}
		case "glob", "grep", "bash":
			fp := name + "=" + strings.TrimSpace(string(args))
			if _, seen := s.calls[fp]; !seen {
				return true
			}
		}
	}
	return false
}

// maybeDedupeRead short-circuits repeated reads of the same path.
// Returns (result, handled). handled=true means caller should use result as tool output.
func (s *thrashState) maybeDedupeRead(args json.RawMessage) (string, bool) {
	if s == nil {
		return "", false
	}
	path := toolArgString(args, "path")
	if path == "" {
		return "", false
	}
	key := strings.TrimSpace(path)
	s.mu.Lock()
	n := s.reads[key]
	s.reads[key] = n + 1
	s.mu.Unlock()
	if n < rereadLimit {
		return "", false
	}
	return fmt.Sprintf(
		"(already read %q this prompt — content unchanged; do not re-read. "+
			"Use the earlier result, then act (edit/write) or finish with goal_report.)",
		key,
	), true
}

// annotateRepeat notes repeated identical name+args for non-read tools (soft footer).
// Also records the call fingerprint for productivity detection on later turns.
func (s *thrashState) annotateRepeat(name string, args json.RawMessage, out string) string {
	if s == nil || !isExploreTool(name) {
		return out
	}
	if name == "read" {
		// path already tracked in maybeDedupeRead / first successful read
		return out
	}
	fp := name + "=" + strings.TrimSpace(string(args))
	s.mu.Lock()
	n := s.calls[fp]
	s.calls[fp] = n + 1
	s.mu.Unlock()
	if n < 1 {
		return out
	}
	return out + fmt.Sprintf(
		"\n\n(note: identical %s call already ran %d time(s) this prompt — "+
			"do not repeat; change approach or finish.)",
		name, n+1,
	)
}

// recordRead marks a path as seen (first successful read that was not short-circuited).
// maybeDedupeRead already increments; this is for the first read path after Exec
// when maybeDedupeRead returned false — actually maybeDedupeRead increments always.
// No separate record needed for read.

func toolArgString(args json.RawMessage, key string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func exploreStopMessage(streak int) string {
	return fmt.Sprintf(
		"stopped after unproductive exploration (streak=%d: re-reading / repeating the same tools) — "+
			"change approach, write/edit, or finish with goal_report",
		streak,
	)
}

func exploreWarnMessage(streak int) string {
	return fmt.Sprintf(
		"Note: %d explore-only tool turns so far. Prefer acting (write/edit) or finishing "+
			"(goal_report) over re-reading files you already have. New files/queries are fine; "+
			"repeating the same read/bash will eventually abort.",
		streak,
	)
}
