package goal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/subosito/mow"
)

// Runner drives Spec / saved State through repeated Engine.Prompt calls.
type Runner struct {
	Engine *mow.Engine
	Store  *Store
	// OnEvent is optional (in addition to package Subscribe listeners).
	OnEvent func(Event)
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
func (r *Runner) Run(ctx context.Context, id string) (State, error) {
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
	return r.runState(ctx, st)
}

// RunSpec creates (or resumes incomplete) then runs.
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
			if prev.MaxSteps <= 0 {
				prev.MaxSteps = spec.MaxSteps
			}
			st = prev
		}
	}
	return r.runState(ctx, st)
}

// RunParallel runs multiple goals concurrently. Each needs its own Engine
// (Prompt is serialized per Engine). newEng is called once per spec.
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

func (r *Runner) runState(ctx context.Context, st State) (State, error) {
	if st.Status == StatusDone {
		// Already complete — do not spend another LLM call.
		r.fire(Event{Kind: EventDone, State: st, Text: "already done"})
		return st, nil
	}
	st.Status = StatusRunning
	st.Error = ""
	if st.SessionID == "" {
		st.SessionID = r.Engine.SessionID()
	}
	if err := r.store().Save(st); err != nil {
		return st, err
	}
	r.fire(Event{Kind: EventStart, State: st, Text: fmt.Sprintf("goal %s start", st.ID)})

	for st.Step < st.MaxSteps {
		if err := ctx.Err(); err != nil {
			st.Status = StatusFailed
			st.Error = err.Error()
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
			return st, err
		}

		sig := &finishSignal{}
		stepCtx := withFinish(ctx, sig)
		prompt := stepPrompt(st)
		res, err := r.Engine.PromptWith(stepCtx, prompt, mow.PromptOpts{
			SystemAppend: SystemAppend(st),
		})
		st.Step++
		st.InputTokens += res.Usage.InputTokens
		st.OutputTokens += res.Usage.OutputTokens
		if st.SessionID == "" {
			st.SessionID = res.SessionID
		}
		if err != nil {
			st.Status = StatusFailed
			st.Error = err.Error()
			st.LastReply = res.Text
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventFail, State: st, Text: err.Error()})
			return st, err
		}
		st.LastReply = res.Text
		done, failed, reason, reportSummary := resolveOutcome(res.Text, sig)
		st.Summary = pickSummary(reportSummary, r.Engine, res.Text)

		if done {
			st.Status = StatusDone
			st.Error = ""
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventDone, State: st, Text: "goal complete"})
			return st, nil
		}
		if failed {
			st.Status = StatusFailed
			if reason == "" {
				reason = "model reported failure"
			}
			st.Error = reason
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventFail, State: st, Text: reason})
			return st, fmt.Errorf("goal failed: %s", reason)
		}

		_ = r.store().Save(st)
		r.fire(Event{
			Kind:  EventStep,
			State: st,
			Text:  fmt.Sprintf("step %d/%d", st.Step, st.MaxSteps),
		})
	}

	st.Status = StatusFailed
	st.Error = fmt.Sprintf("max steps %d exceeded", st.MaxSteps)
	_ = r.store().Save(st)
	r.fire(Event{Kind: EventFail, State: st, Text: st.Error})
	return st, fmt.Errorf("goal: %s", st.Error)
}

func resolveOutcome(text string, sig *finishSignal) (done, failed bool, reason, summary string) {
	if sig != nil {
		if d, f, r, s := sig.outcome(); d || f {
			return d, f, r, s
		}
	}
	if d, f, r, s := ParseStatusJSON(text); d || f {
		return d, f, r, s
	}
	d, f, r := ParseOutcome(text)
	return d, f, r, ""
}

// pickSummary prefers goal_report.summary, then the best assistant prose from the
// last Prompt's message history (models often put the real answer before a bare
// GOAL_DONE line). Transcript alone is insufficient — it only stores the final text.
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
		return "Begin work on the goal.\n\nGoal:\n" + st.Goal
	}
	var b strings.Builder
	b.WriteString("Continue work on the goal.\n\nGoal:\n")
	b.WriteString(st.Goal)
	if s := strings.TrimSpace(st.Summary); s != "" {
		b.WriteString("\n\nPrevious step result (truncated):\n")
		b.WriteString(s)
	}
	b.WriteString("\n\nMake the next concrete progress.")
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
