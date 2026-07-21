package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/subosito/mow/internal/llm"
)

// Soft exploration helpers — no hard kill. Long autonomous runs (hours/days)
// stop on: model finishes, ctx cancel, or MaxTurns when the user set one.
// These only (1) stub re-reads and (2) inject occasional wrap-up nudges.
const (
	// exploreWarnEvery injects a wrap-up nudge every N explore-only turns.
	exploreWarnEvery = 20
	// rereadLimit: after this many successful reads of the same path in one
	// Prompt, further reads return a short stub instead of the full file.
	rereadLimit = 1
	// sameToolWarnAfter injects a nudge when the identical tool batch repeats.
	sameToolWarnAfter = 3
)

// thrashState tracks per-Prompt exploration for soft hints only.
type thrashState struct {
	mu sync.Mutex
	// path → times successfully read this Prompt
	reads map[string]int
	// exact tool name+args → times (glob/bash/grep)
	calls map[string]int
	// consecutive turns whose tools were all explore-only
	exploreStreak int
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

// noteTurn updates explore streak. Returns whether to inject a soft wrap-up warn.
func (s *thrashState) noteTurn(calls []llm.ToolCall) (warn bool) {
	if s == nil {
		return false
	}
	if !batchExploreOnly(calls) {
		s.exploreStreak = 0
		return false
	}
	s.exploreStreak++
	return s.exploreStreak > 0 && s.exploreStreak%exploreWarnEvery == 0
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
			"Use the earlier result, then act (edit/write) or finish.)",
		key,
	), true
}

// annotateRepeat notes repeated identical name+args for non-read tools (soft footer).
func (s *thrashState) annotateRepeat(name string, args json.RawMessage, out string) string {
	if s == nil || !isExploreTool(name) || name == "read" {
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

func exploreWarnMessage(streak int) string {
	return fmt.Sprintf(
		"Note: %d explore-only tool turns so far. Prefer acting (write/edit/test) or finishing "+
			"over re-reading files you already have. This is a hint only — the run continues.",
		streak,
	)
}

func sameToolWarnMessage(n int) string {
	return fmt.Sprintf(
		"You repeated the same tool call(s) %d times. Change args, act (write/edit), or finish — "+
			"the run is not stopped; avoid tight loops.",
		n,
	)
}
