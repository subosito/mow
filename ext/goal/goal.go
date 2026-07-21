// Package goal is a headless outer loop over mow.Engine: work a goal across
// multiple Prompt turns until done, failed, or max steps.
//
//	import _ "github.com/subosito/mow/ext/goal"   // registers `mow goal`
//
// Core stays one Prompt / one tool loop. This pack only orchestrates.
// Hosts (RPC, embedders, …) may Subscribe to events; none are required.
package goal

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status is the lifecycle of a goal.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Spec is the input to create / run a goal.
type Spec struct {
	// ID is a filesystem-safe name (slug). Empty → derived from Goal.
	ID string
	// Goal is the natural-language objective.
	Goal string
	// MaxSteps caps Prompt iterations (default 8).
	MaxSteps int
}

// State is durable progress (JSON under $MOW_HOME/goals/<id>.json).
type State struct {
	ID        string    `json:"id"`
	Goal      string    `json:"goal"`
	Status    Status    `json:"status"`
	Step      int       `json:"step"`
	MaxSteps  int       `json:"max_steps"`
	SessionID string    `json:"session_id,omitempty"`
	LastReply string    `json:"last_reply,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// InputTokens / OutputTokens are cumulative across all steps (zero when
	// the provider reports no usage).
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// EventKind classifies progress signals for subscribers (logs, hosts, …).
type EventKind string

const (
	EventStart EventKind = "start"
	EventStep  EventKind = "step"
	EventDone  EventKind = "done"
	EventFail  EventKind = "fail"
)

// Event is a progress notification. Safe for concurrent subscribers.
type Event struct {
	Kind  EventKind
	State State
	// Text is a short human line (e.g. step summary).
	Text string
}

// Model markers in the final assistant text (own line) to end the outer loop.
const (
	MarkerDone   = "GOAL_DONE"
	MarkerFailed = "GOAL_FAILED:"
)

var (
	subMu sync.Mutex
	subs  []func(Event)
)

// Subscribe registers a listener for all goal events from this process.
// Used by optional UIs; headless runs need none. Returns unsubscribe.
func Subscribe(fn func(Event)) (unsubscribe func()) {
	if fn == nil {
		return func() {}
	}
	subMu.Lock()
	subs = append(subs, fn)
	i := len(subs) - 1
	subMu.Unlock()
	return func() {
		subMu.Lock()
		defer subMu.Unlock()
		if i >= 0 && i < len(subs) {
			subs[i] = nil
		}
	}
}

func emit(e Event, extra func(Event)) {
	if extra != nil {
		extra(e)
	}
	subMu.Lock()
	cp := make([]func(Event), len(subs))
	copy(cp, subs)
	subMu.Unlock()
	for _, fn := range cp {
		if fn != nil {
			fn(e)
		}
	}
}

// NormalizeSpec fills defaults and validates.
func NormalizeSpec(s Spec) (Spec, error) {
	s.Goal = strings.TrimSpace(s.Goal)
	if s.Goal == "" {
		return s, fmt.Errorf("goal: empty goal text")
	}
	s.ID = strings.TrimSpace(s.ID)
	if s.ID == "" {
		s.ID = slugID(s.Goal)
	}
	if err := validateID(s.ID); err != nil {
		return s, err
	}
	if s.MaxSteps <= 0 {
		s.MaxSteps = 8
	}
	if s.MaxSteps > 64 {
		s.MaxSteps = 64
	}
	return s, nil
}

func validateID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("goal: invalid id %q", id)
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("goal: id %q has invalid character %q", id, r)
	}
	return nil
}

func slugID(goal string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(goal) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
				b.WriteByte('-')
			}
		}
		if b.Len() >= 32 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = fmt.Sprintf("goal-%d", time.Now().Unix())
	}
	return s
}

// ParseOutcome inspects assistant text for completion markers.
// Returns (done, failed, reason). reason is non-empty only on failed.
func ParseOutcome(text string) (done, failed bool, reason string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == MarkerDone || strings.HasPrefix(line, MarkerDone+" ") {
			return true, false, ""
		}
		if strings.HasPrefix(line, MarkerFailed) {
			return false, true, strings.TrimSpace(strings.TrimPrefix(line, MarkerFailed))
		}
	}
	return false, false, ""
}

// contentWithoutMarkers strips completion markers so leftover prose can be a summary.
func contentWithoutMarkers(text string) string {
	var b strings.Builder
	// Drop goal-status fences.
	for {
		i := strings.Index(text, "```goal-status")
		if i < 0 {
			break
		}
		rest := text[i+len("```goal-status"):]
		end := strings.Index(rest, "```")
		if end < 0 {
			text = text[:i]
			break
		}
		text = text[:i] + rest[end+3:]
	}
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if trim == MarkerDone || strings.HasPrefix(trim, MarkerDone+" ") {
			continue
		}
		if strings.HasPrefix(trim, MarkerFailed) {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

// SystemAppend is injected each step (via PromptOpts) for the outer-loop protocol.
func SystemAppend(st State) string {
	return fmt.Sprintf(
		"You are working toward THIS goal only (outer loop step %d of %d):\n%s\n\n"+
			"The summary you report must answer THIS goal — ignore unrelated prior chat or git leftovers.\n\n"+
			"Finish protocol (required):\n"+
			"- When THIS goal is met, call tool goal_report with status=done AND summary= the "+
			"user-facing result for this goal. That ends the step — do not keep calling tools after.\n"+
			"- Prefer goal_report over bare %s. "+
			"Alternatively end with ```goal-status {\"status\":\"done\",\"summary\":\"…\"} ```.\n"+
			"- If blocked, goal_report status=failed with reason (or %s <reason>).\n\n"+
			"Working rules:\n"+
			"- Make focused progress; do not re-read or re-list files you already inspected.\n"+
			"- Avoid endless bash explore loops (find/ls/cat cycles). Read what you need, then act or finish.\n"+
			"- Do not claim done until THIS goal is actually met.",
		st.Step+1, st.MaxSteps, st.Goal, MarkerDone, MarkerFailed,
	)
}
