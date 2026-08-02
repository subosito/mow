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
	// StatusPartial: budget exhausted with a usable result — the run stopped
	// cleanly and State.Partial summarizes what exists vs what is missing.
	StatusPartial Status = "partial"
	// StatusBlocked: the goal paused for a human decision (escalate route).
	StatusBlocked Status = "blocked"
)

// Step budget defaults. A step is one full Prompt (itself up to
// policy.max_turns tool round-trips), so these are outer iterations, not
// tool calls.
const (
	// DefaultMaxSteps is the default outer budget. Real multi-phase work
	// ("make CI green": explore → fix → retest → lint → fix again) commonly
	// needs a dozen-plus steps; 8 stopped useful runs mid-flight and forced a
	// resume just to continue.
	DefaultMaxSteps = 16
	// MaxMaxSteps is the hard ceiling a Spec may request.
	MaxMaxSteps = 64
)

// Spec is the input to create / run a goal.
type Spec struct {
	// ID is a filesystem-safe name (slug). Empty → derived from Goal.
	ID string
	// Goal is the natural-language objective.
	Goal string
	// MaxSteps caps Prompt iterations (default DefaultMaxSteps).
	MaxSteps int
	// ParallelMax opts into intra-goal parallelism: 0/1 = sequential (the
	// default and unchanged behavior); N > 1 runs up to N independent pending
	// plan items as concurrent sub-steps, each on its own Engine. Requires
	// Runner.EngineFactory (one mow.Engine serializes Prompt calls); without
	// a factory the runner stays sequential. Capped by MaxParallelWidth.
	ParallelMax int
}

// Fact is one durable evidence item recorded by a goal step (goal_report
// evidence=...). Facts are the "state outside the chat window": the next step
// sees selected facts, not the whole transcript.
type Fact struct {
	ID     string `json:"id,omitempty"`
	Claim  string `json:"claim"`
	Source string `json:"source,omitempty"`
	// Confidence 0-1; 0 = unset.
	Confidence float64 `json:"confidence,omitempty"`
	// ProducedByStep is the outer-loop step that recorded this fact.
	ProducedByStep int `json:"produced_by_step,omitempty"`
}

// Facts renders the durable evidence lines (compact, newest last).
func (st State) FactsText() string {
	if len(st.Facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range st.Facts {
		line := strings.TrimSpace(f.Claim)
		if line == "" {
			continue
		}
		if f.Source != "" {
			line += " (source: " + strings.TrimSpace(f.Source) + ")"
		}
		if f.Confidence > 0 {
			line += fmt.Sprintf(" [%.0f%%]", f.Confidence*100)
		}
		b.WriteString("- " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// State is durable progress (JSON under $MOW_HOME/goals/<id>.json).
type State struct {
	ID   string `json:"id"`
	Goal string `json:"goal"`
	// Workspace keys optional cross-run evidence; empty preserves legacy states.
	Workspace string `json:"workspace,omitempty"`
	Status    Status `json:"status"`
	Step      int    `json:"step"`
	MaxSteps  int    `json:"max_steps"`
	// ParallelMax mirrors Spec.ParallelMax (0/1 = sequential).
	ParallelMax int    `json:"parallel_max,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	LastReply   string `json:"last_reply,omitempty"`
	Summary     string `json:"summary,omitempty"`
	// Facts is the durable evidence ledger (graph state outside the window).
	Facts []Fact `json:"facts,omitempty"`
	// RetryCount counts consecutive retry_same steps (code-owned cap).
	RetryCount int `json:"retry_count,omitempty"`
	// Question is the durable human decision when Status == StatusBlocked.
	Question string `json:"question,omitempty"`
	// VerifyNote carries the verifier's feedback for the next retry step.
	VerifyNote string `json:"verify_note,omitempty"`
	// Partial is a machine-readable summary when Status == StatusPartial:
	// what is done, what is missing, and the best artifact so far.
	Partial string `json:"partial,omitempty"`
	Error   string `json:"error,omitempty"`
	// Plan is an optional checklist. When set, status=done requires all items done/skipped.
	Plan Plan `json:"plan,omitempty"`
	// CurrentItem is the plan item id this step should focus (hint; empty = next pending).
	CurrentItem string    `json:"current_item,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	// InputTokens / OutputTokens are cumulative across all steps (zero when
	// the provider reports no usage).
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// NodeStatus is the frozen host contract for one goal-plan node. Hosts should
// derive task maps from State.Nodes rather than parsing prompt checklist prose.
type NodeStatus struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// Nodes returns the ordered node-status projection. It is derived from durable
// State fields, so adding it requires no stored-state migration. A goal without
// a checklist is represented by one synthetic "goal" node.
func (st State) Nodes() []NodeStatus {
	if !st.Plan.HasItems() {
		status := string(st.Status)
		if status == "" {
			status = string(StatusPending)
		}
		return []NodeStatus{{ID: "goal", Title: strings.TrimSpace(st.Goal), Status: status}}
	}
	nodes := make([]NodeStatus, 0, len(st.Plan.Items))
	for _, item := range st.Plan.Items {
		status := string(item.Status)
		if status == "" {
			status = string(ItemPending)
		}
		nodes = append(nodes, NodeStatus{ID: item.ID, Title: item.Title, Status: status})
	}
	return nodes
}

// NodeSummary returns the bounded one-line current-node prompt injection.
func (st State) NodeSummary() string {
	nodes := st.Nodes()
	idx := 0
	if st.CurrentItem != "" {
		for i := range nodes {
			if nodes[i].ID == st.CurrentItem {
				idx = i
				break
			}
		}
	} else if st.Plan.HasItems() {
		idx = len(nodes) - 1
		for i := range nodes {
			if nodes[i].Status == string(ItemPending) {
				idx = i
				break
			}
		}
	}
	n := nodes[idx]
	title := compactNodeTitle(n.Title, 80)
	if title == "" {
		title = n.ID
	}
	return fmt.Sprintf("node %d/%d [%s] %s (evidence: %d)", idx+1, len(nodes), n.ID, title, len(st.Facts))
}

func compactNodeTitle(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// EventKind classifies progress signals for subscribers (logs, hosts, …).
type EventKind string

const (
	EventStart   EventKind = "start"
	EventStep    EventKind = "step"
	EventDone    EventKind = "done"
	EventFail    EventKind = "fail"
	EventPartial EventKind = "partial"
	// EventBlocked: the goal paused for a human decision (escalate).
	EventBlocked EventKind = "blocked"
)

// Event is a progress notification. Safe for concurrent subscribers.
// State carries the frozen Nodes projection for hosts; no separate node event is needed.
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
		s.MaxSteps = DefaultMaxSteps
	}
	if s.MaxSteps > MaxMaxSteps {
		s.MaxSteps = MaxMaxSteps
	}
	if s.ParallelMax < 0 {
		s.ParallelMax = 0
	}
	if s.ParallelMax > MaxParallelWidth {
		s.ParallelMax = MaxParallelWidth
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
	var b strings.Builder
	fmt.Fprintf(&b, "Current node: %s\n\n", st.NodeSummary())
	fmt.Fprintf(&b, "You are working toward THIS goal only (outer loop step %d of %d):\n%s\n\n",
		st.Step+1, st.MaxSteps, st.Goal)
	b.WriteString("The summary you report must answer THIS goal — ignore unrelated prior chat. Do not discard uncommitted work to tidy the tree.\n\n")
	if st.Plan.HasItems() {
		b.WriteString("Checklist:\n")
		b.WriteString(st.Plan.Format())
		b.WriteString("\n\n")
		if item, ok := st.Plan.NextPending(); ok {
			fmt.Fprintf(&b, "Focus this step on: [%s] %s\n", item.ID, item.Title)
			b.WriteString("When that item is done: goal_report status=continue item_id=" + item.ID + " item_status=done item_note=…\n")
			b.WriteString("When ALL items are done/skipped: goal_report status=done summary=…\n\n")
		} else if st.Plan.AllDone() {
			b.WriteString("All checklist items are done — call goal_report status=done summary=…\n\n")
		}
	} else {
		b.WriteString("No checklist yet. Early in the goal, call goal_report status=continue with plan=[{id,title,status:pending},…] " +
			"to break the goal into concrete items (recommended for multi-part work).\n")
		b.WriteString("Or if the goal is trivial, goal_report status=done summary=… immediately.\n\n")
	}
	fmt.Fprintf(&b,
		"Evidence: record durable facts with goal_report evidence=[{claim,source,confidence}] — \n"+
			"later steps see the evidence ledger, not the whole conversation.\n\n"+
			"Finish protocol:\n"+
			"- goal_report status=done summary=… (preferred over bare %s). Checklist must be complete if present.\n"+
			"- goal_report status=failed reason=… (or %s <reason>).\n"+
			"- goal_report status=continue for progress / plan / item updates.\n"+
			"- Long-lived servers: goal_process_start / status / stop (not bare bash &).\n"+
			"- Do not nest another agent loop or outer goal runner inside bash.\n"+
			"- Do not claim done until THIS goal is actually met.\n",
		MarkerDone, MarkerFailed)
	return b.String()
}
