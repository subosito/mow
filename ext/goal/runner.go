package goal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/subosito/mow"
)

// maxTransientSteps fails after this many consecutive steps that hit a
// transient LLM/gateway error (502/503/429/…) after the HTTP client already
// retried. One blip must not kill a multi-hour feature-test goal.
const maxTransientSteps = 5

// maxTurnBudgetSteps soft-continues when a step hits a user-set MaxTurns budget.
// After this many consecutive budget hits, fail.
const maxTurnBudgetSteps = 5

// transientBackoff waits before the next step after an upstream blip.
// Overridable in tests (set to 0 for speed).
var transientBackoff = 2 * time.Second

// Runner drives Spec / saved State through repeated Executor steps.
type Runner struct {
	Engine *mow.Engine
	Store  *Store
	// OnEvent is optional (in addition to package Subscribe listeners).
	OnEvent func(Event)
	// Exec optional; default builds Executor from Engine + Store.
	Exec *Executor
}

// Create normalizes Spec and persists a pending goal.
func (r *Runner) Create(spec Spec) (State, error) {
	spec, err := NormalizeSpec(spec)
	if err != nil {
		return State{}, err
	}
	store := r.store()
	st := State{
		ID:       spec.ID,
		Goal:     spec.Goal,
		Status:   StatusPending,
		Step:     0,
		MaxSteps: spec.MaxSteps,
	}
	if err := store.Save(st); err != nil {
		return State{}, err
	}
	return st, nil
}

// Run executes steps until done, failed, max steps, or ctx cancel.
// maxStepsRaise, when > stored MaxSteps, raises the outer budget so a failed
// "max steps exceeded" goal can continue (e.g. CLI --max-steps 24).
func (r *Runner) Run(ctx context.Context, id string) (State, error) {
	return r.RunRaise(ctx, id, 0)
}

// RunRaise is Run with an optional MaxSteps raise (0 = keep stored).
func (r *Runner) RunRaise(ctx context.Context, id string, maxStepsRaise int) (State, error) {
	if r == nil || r.Engine == nil {
		return State{}, fmt.Errorf("goal: nil engine")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return State{}, fmt.Errorf("goal: empty id")
	}
	st, err := r.store().Load(id)
	if err != nil {
		return State{}, err
	}
	st = applyMaxStepsRaise(st, maxStepsRaise)
	return r.runState(ctx, st)
}

// RunSpec creates (or resumes incomplete) then runs.
// On resume, MaxSteps is the max of stored and spec.MaxSteps so CLI can raise the budget.
func (r *Runner) RunSpec(ctx context.Context, spec Spec) (State, error) {
	if r == nil || r.Engine == nil {
		return State{}, fmt.Errorf("goal: nil engine")
	}
	spec, err := NormalizeSpec(spec)
	if err != nil {
		return State{}, err
	}
	st := State{
		ID:       spec.ID,
		Goal:     spec.Goal,
		Status:   StatusPending,
		Step:     0,
		MaxSteps: spec.MaxSteps,
	}
	if prev, err := r.store().Load(spec.ID); err == nil {
		if prev.Status == StatusRunning || prev.Status == StatusPending || prev.Status == StatusFailed {
			if strings.TrimSpace(prev.Goal) == "" {
				prev.Goal = spec.Goal
			}
			prev = applyMaxStepsRaise(prev, spec.MaxSteps)
			st = prev
		}
	}
	return r.runState(ctx, st)
}

// applyMaxStepsRaise raises st.MaxSteps when raise > current (never lowers).
// Clears a pure "max steps exceeded" failure so the outer loop can continue.
func applyMaxStepsRaise(st State, raise int) State {
	if raise <= st.MaxSteps {
		return st
	}
	st.MaxSteps = raise
	if st.Status == StatusFailed && strings.Contains(st.Error, "max steps") {
		st.Error = ""
		// runState sets StatusRunning; leave Failed→clear so we don't look terminal mid-load.
		st.Status = StatusPending
	}
	return st
}

// RunParallel runs multiple goals concurrently. Each needs its own Engine.
func RunParallel(ctx context.Context, specs []Spec, newEng func() (*mow.Engine, error), store *Store) ([]State, error) {
	if newEng == nil {
		return nil, fmt.Errorf("goal: nil engine factory")
	}
	if store == nil {
		store = &Store{}
	}
	out := make([]State, len(specs))
	errs := make([]error, len(specs))
	var wg sync.WaitGroup
	for i := range specs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			eng, err := newEng()
			if err != nil {
				errs[i] = err
				out[i] = State{ID: specs[i].ID, Status: StatusFailed, Error: err.Error()}
				return
			}
			r := &Runner{Engine: eng, Store: store}
			st, err := r.RunSpec(ctx, specs[i])
			out[i] = st
			errs[i] = err
		}(i)
	}
	wg.Wait()
	var first error
	for _, e := range errs {
		if e != nil {
			first = e
			break
		}
	}
	return out, first
}

func (r *Runner) store() *Store {
	if r.Store != nil {
		return r.Store
	}
	return &Store{}
}

func (r *Runner) executor() *Executor {
	if r.Exec != nil {
		return r.Exec
	}
	return &Executor{Engine: r.Engine, StoreDir: r.store().dir()}
}

func (r *Runner) runState(ctx context.Context, st State) (State, error) {
	if st.Status == StatusDone {
		r.fire(Event{Kind: EventDone, State: st, Text: "already done"})
		return st, nil
	}
	st.Status = StatusRunning
	st.Error = ""
	if st.SessionID == "" && r.Engine != nil {
		st.SessionID = r.Engine.SessionID()
	}
	if err := r.store().Save(st); err != nil {
		return st, err
	}
	r.fire(Event{Kind: EventStart, State: st, Text: fmt.Sprintf("goal %s start", st.ID)})
	r.store().AppendEvent(st.ID, LogEvent{Kind: "start", Status: st.Status, Step: st.Step, Plan: planPtr(st.Plan)})

	transientSteps := 0
	turnBudgetSteps := 0
	exec := r.executor()

	for st.Step < st.MaxSteps {
		if err := ctx.Err(); err != nil {
			st.Status = StatusFailed
			st.Error = err.Error()
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "fail", Status: st.Status, Step: st.Step, Error: st.Error})
			return st, err
		}

		// Hint focus item for prompts.
		if item, ok := st.Plan.NextPending(); ok {
			st.CurrentItem = item.ID
		} else {
			st.CurrentItem = ""
		}

		sr, err := exec.RunStep(ctx, st)
		st.Step++
		st.InputTokens += sr.Usage.InputTokens
		st.OutputTokens += sr.Usage.OutputTokens
		if st.SessionID == "" {
			st.SessionID = sr.SessionID
		}
		if sr.Plan.HasItems() {
			st.Plan = sr.Plan
		}

		if err != nil {
			if errors.Is(err, mow.ErrAgentMaxTurns) {
				turnBudgetSteps++
				transientSteps = 0
				st.LastReply = sr.Text
				st.Summary = maxTurnsStepSummary(r.Engine, sr.Text)
				st.Error = ""
				if turnBudgetSteps >= maxTurnBudgetSteps {
					st.Status = StatusFailed
					st.Error = classifyStepError(err) + fmt.Sprintf(" (%d steps)", turnBudgetSteps)
					_ = r.store().Save(st)
					r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
					r.store().AppendEvent(st.ID, LogEvent{Kind: "fail", Status: st.Status, Step: st.Step, Error: st.Error, Outcome: string(OutcomeBudget)})
					return st, fmt.Errorf("goal: %s", st.Error)
				}
				_ = r.store().Save(st)
				r.fire(Event{Kind: EventStep, State: st, Text: fmt.Sprintf("step %d/%d (max turns — continuing)", st.Step, st.MaxSteps)})
				r.store().AppendEvent(st.ID, LogEvent{Kind: "budget", Step: st.Step, Text: st.Summary, Plan: planPtr(st.Plan)})
				continue
			}
			if isTransientLLM(err) {
				transientSteps++
				turnBudgetSteps = 0
				st.LastReply = sr.Text
				st.Summary = transientStepSummary(err, r.Engine, sr.Text)
				st.Error = ""
				if transientSteps >= maxTransientSteps {
					st.Status = StatusFailed
					st.Error = classifyStepError(err) + fmt.Sprintf(" (%d consecutive upstream failures)", transientSteps)
					_ = r.store().Save(st)
					r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
					r.store().AppendEvent(st.ID, LogEvent{Kind: "fail", Status: st.Status, Step: st.Step, Error: st.Error, Outcome: string(OutcomeRetry)})
					return st, fmt.Errorf("goal: %s", st.Error)
				}
				_ = r.store().Save(st)
				r.fire(Event{Kind: EventStep, State: st, Text: fmt.Sprintf("step %d/%d (LLM upstream blip — retrying)", st.Step, st.MaxSteps)})
				r.store().AppendEvent(st.ID, LogEvent{Kind: "retry", Step: st.Step, Text: st.Summary, Error: err.Error()})
				select {
				case <-ctx.Done():
					st.Status = StatusFailed
					st.Error = ctx.Err().Error()
					_ = r.store().Save(st)
					r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
					return st, ctx.Err()
				case <-time.After(transientBackoff):
				}
				continue
			}
			st.Status = StatusFailed
			st.Error = classifyStepError(err)
			st.LastReply = sr.Text
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "fail", Status: st.Status, Step: st.Step, Error: st.Error})
			return st, fmt.Errorf("goal: %s", st.Error)
		}

		turnBudgetSteps = 0
		transientSteps = 0
		st.LastReply = sr.Text
		if sr.Summary != "" {
			st.Summary = sr.Summary
		} else {
			st.Summary = pickSummary("", r.Engine, sr.Text)
		}

		switch sr.Outcome {
		case OutcomeDone:
			st.Status = StatusDone
			st.Error = ""
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventDone, State: st, Text: "goal complete"})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "done", Status: st.Status, Step: st.Step, Text: st.Summary, Plan: planPtr(st.Plan), Outcome: string(OutcomeDone)})
			return st, nil
		case OutcomeFailed:
			st.Status = StatusFailed
			if sr.Reason == "" {
				st.Error = "model reported failure"
			} else {
				st.Error = sr.Reason
			}
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "fail", Status: st.Status, Step: st.Step, Error: st.Error, Outcome: string(OutcomeFailed)})
			return st, fmt.Errorf("goal failed: %s", st.Error)
		default:
			// continue
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventStep, State: st, Text: fmt.Sprintf("step %d/%d", st.Step, st.MaxSteps)})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "step", Status: st.Status, Step: st.Step, Text: st.Summary, Plan: planPtr(st.Plan), Outcome: string(OutcomeContinue)})
		}
	}

	st.Status = StatusFailed
	st.Error = fmt.Sprintf("max steps %d exceeded", st.MaxSteps)
	_ = r.store().Save(st)
	r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
	r.store().AppendEvent(st.ID, LogEvent{Kind: "fail", Status: st.Status, Step: st.Step, Error: st.Error})
	return st, fmt.Errorf("goal: %s", st.Error)
}

func planPtr(p Plan) *Plan {
	if !p.HasItems() {
		return nil
	}
	cp := p
	cp.Items = append([]PlanItem(nil), p.Items...)
	return &cp
}

// classifyStepError maps agent loop errors into clearer goal failure text.
func classifyStepError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, mow.ErrAgentStuck):
		return "stuck: unproductive exploration — change approach or finish with goal_report"
	case errors.Is(err, mow.ErrAgentMaxTurns):
		return "step hit max agent turns (tool-loop budget)"
	case isTransientLLM(err):
		return "LLM upstream error after retries: " + err.Error()
	case errors.Is(err, context.Canceled):
		return err.Error()
	default:
		return err.Error()
	}
}

func isTransientLLM(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, p := range []string{
		"http 429", "http 502", "http 503", "http 504", "http 500",
		"upstream error", "bad gateway", "service unavailable", "gateway timeout",
		"too many requests", "connection reset", "connection refused",
		"tls handshake timeout", "i/o timeout", "eof",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func maxTurnsStepSummary(eng *mow.Engine, finalText string) string {
	const note = "Previous step hit the agent turn budget. Continue from progress; finish one checklist item, then goal_report."
	if s := pickSummary("", eng, finalText); s != "" {
		return truncateRunes(note+"\n\n"+s, 2000)
	}
	return note
}

func transientStepSummary(err error, eng *mow.Engine, finalText string) string {
	note := "Previous step hit a transient LLM/gateway error (" + err.Error() + "). Retry the same work."
	if s := pickSummary("", eng, finalText); s != "" {
		return truncateRunes(note+"\n\n"+s, 2000)
	}
	return note
}

// pickSummary prefers report summary, then best assistant prose from history.
func pickSummary(reportSummary string, eng *mow.Engine, finalText string) string {
	if s := strings.TrimSpace(reportSummary); s != "" {
		return truncateRunes(s, 2000)
	}
	if eng != nil {
		msgs := eng.Messages()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role != "assistant" {
				continue
			}
			if s := contentWithoutMarkers(msgs[i].Content); s != "" {
				return truncateRunes(s, 2000)
			}
		}
	}
	if s := contentWithoutMarkers(finalText); s != "" {
		return truncateRunes(s, 2000)
	}
	return truncateRunes(finalText, 400)
}

func (r *Runner) fire(e Event) {
	emit(e, r.OnEvent)
}

func stepPrompt(st State) string {
	if st.Step == 0 {
		var b strings.Builder
		b.WriteString("Begin work on the goal.\n\nGoal:\n")
		b.WriteString(st.Goal)
		b.WriteString("\n\nIf the goal has multiple parts, first call goal_report status=continue with plan=[...] (id+title+status=pending). ")
		b.WriteString("Otherwise call goal_report status=done summary=… when finished.")
		return b.String()
	}
	var b strings.Builder
	b.WriteString("Continue work on the goal.\n\nGoal:\n")
	b.WriteString(st.Goal)
	if st.Plan.HasItems() {
		b.WriteString("\n\nChecklist:\n")
		b.WriteString(st.Plan.Format())
		if item, ok := st.Plan.NextPending(); ok {
			fmt.Fprintf(&b, "\n\nFocus: [%s] %s\nMark done with goal_report status=continue item_id=%s item_status=done.",
				item.ID, item.Title, item.ID)
		} else if st.Plan.AllDone() {
			b.WriteString("\n\nAll items done — call goal_report status=done summary=…")
		}
	}
	if s := strings.TrimSpace(st.Summary); s != "" {
		b.WriteString("\n\nPrevious step result (truncated):\n")
		b.WriteString(s)
	}
	b.WriteString("\n\nDo not re-read files already covered. If the whole goal is met, goal_report status=done summary=…")
	return b.String()
}

func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}
