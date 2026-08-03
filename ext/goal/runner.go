package goal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	// EngineFactory supplies a FRESH engine per concurrent sub-step when a
	// goal opts into intra-goal parallelism (Spec.ParallelMax > 1). It is
	// required for parallelism because mow.Engine serializes Prompt calls
	// (promptMu): one engine cannot run two steps at once. Build it exactly
	// like RunParallel's newEng (mow.New with the host's Options). Nil =
	// the runner always runs sequentially, whatever ParallelMax says.
	EngineFactory func() (*mow.Engine, error)
	// WorktreeEngineFactory supplies an engine whose workspace is dir, for
	// plan items marked Worker: WorkerWorktree. Build it like EngineFactory
	// but with Options.Workspace = dir, so the sub-engine's tools and path
	// jail operate inside the isolated checkout. Nil = worktree items run as
	// ordinary steps in the goal workspace (with a note).
	WorktreeEngineFactory func(dir string) (*mow.Engine, error)
	// Router is the code-owned edge: when non-nil and returns a non-zero
	// outcome, it OVERRIDES the model-decided outcome for this step
	// ("do not use an LLM to decide what ordinary code already knows").
	// A zero outcome lets the model's decision stand.
	Router func(st State, sr StepResult) StepOutcome
	// Verify is the deterministic gate between steps (the reusable
	// verification primitive): when non-nil, it checks the step's artifacts /
	// evidence and returns concrete issues. Non-empty feedback forces the
	// next iteration to retry_same with the feedback injected into the prompt
	// (bounded by MaxStepRetries), so a step cannot pass an invalid result
	// onward. Nil = no verification gate.
	Verify func(st State, sr StepResult) []string
}

// MaxStepRetries is the code-owned cap on consecutive retry_same steps; past
// it the goal stops partial instead of looping forever.
const MaxStepRetries = 3

// Create normalizes Spec and persists a pending goal.
func (r *Runner) Create(spec Spec) (State, error) {
	spec, err := NormalizeSpec(spec)
	if err != nil {
		return State{}, err
	}
	store := r.store()
	st := State{
		ID:        spec.ID,
		Goal:      spec.Goal,
		Workspace: r.workspace(),
		Status:    StatusPending,
		Step:      0,
		MaxSteps:  spec.MaxSteps,

		ParallelMax: spec.ParallelMax,
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

// ResumeAnswer unblocks an escalated goal (StatusBlocked): the human answer
// is appended to state, the question is cleared, and the run continues. The
// answer lands in the goal's Summary prefix so the next step sees it.
func (r *Runner) ResumeAnswer(ctx context.Context, id, answer string) (State, error) {
	if r == nil || r.Engine == nil {
		return State{}, fmt.Errorf("goal: nil engine")
	}
	id = strings.TrimSpace(id)
	answer = strings.TrimSpace(answer)
	if id == "" || answer == "" {
		return State{}, fmt.Errorf("goal: resume --answer required")
	}
	st, err := r.store().Load(id)
	if err != nil {
		return State{}, err
	}
	if st.Status != StatusBlocked {
		return State{}, fmt.Errorf("goal %s is %s (not blocked — nothing to answer)", id, st.Status)
	}
	// Durable record of the human decision.
	st.Facts = append(st.Facts, Fact{
		Claim:          "human decision: " + answer,
		ProducedByStep: st.Step,
	})
	if st.Summary != "" {
		st.Summary = st.Summary + "\n[h: " + answer + "]"
	} else {
		st.Summary = "[h: " + answer + "]"
	}
	st.Question = ""
	st.Status = StatusRunning
	if err := r.store().Save(st); err != nil {
		return State{}, err
	}
	r.fire(Event{Kind: EventStep, State: st, Text: "escalation answered: " + answer})
	r.store().AppendEvent(st.ID, LogEvent{Kind: "resume", Status: st.Status, Step: st.Step, Text: answer, Plan: planPtr(st.Plan)})
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
		ID:        spec.ID,
		Goal:      spec.Goal,
		Workspace: r.workspace(),
		Status:    StatusPending,
		Step:      0,
		MaxSteps:  spec.MaxSteps,

		ParallelMax: spec.ParallelMax,
	}
	if prev, err := r.store().Load(spec.ID); err == nil {
		if prev.Status == StatusRunning || prev.Status == StatusPending || prev.Status == StatusFailed {
			if strings.TrimSpace(prev.Goal) == "" {
				prev.Goal = spec.Goal
			}
			if strings.TrimSpace(prev.Workspace) == "" {
				prev.Workspace = r.workspace()
			}
			prev = applyMaxStepsRaise(prev, spec.MaxSteps)
			if spec.ParallelMax > 0 {
				prev.ParallelMax = spec.ParallelMax
			}
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
			defer eng.Close()
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

		var (
			sr  StepResult
			err error
		)
		if width := r.parallelWidth(st); width > 1 {
			r.fire(Event{Kind: EventStep, State: st, Text: fmt.Sprintf("step %d/%d — %d items in parallel", st.Step+1, st.MaxSteps, width)})
			sr, err = r.runParallelStep(ctx, exec, st, width)
		} else if item, ok := st.Plan.NextPending(); ok && isWorktreeItem(item) {
			// Sequential worktree item: isolate, commit, merge back.
			sr, err = r.runWorktreeItem(ctx, exec, st, item)
		} else {
			sr, err = exec.RunStep(ctx, st)
		}
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
		st.VerifyNote = "" // verification passed (or no gate)
		st.LastReply = sr.Text
		if sr.Summary != "" {
			st.Summary = sr.Summary
		} else {
			st.Summary = pickSummary("", r.Engine, sr.Text)
		}
		// Evidence ledger: append this step's facts (dedupe by claim) so the
		// next step sees structured facts, not conversational debris.
		for _, f := range sr.Evidence {
			claim := strings.TrimSpace(f.Claim)
			if claim == "" {
				continue
			}
			f.ProducedByStep = st.Step
			f.Claim = claim
			dup := false
			for i := range st.Facts {
				if st.Facts[i].Claim == claim {
					st.Facts[i] = f
					dup = true
					break
				}
			}
			if !dup {
				st.Facts = append(st.Facts, f)
			}
		}

		// Code-owned edge: a Router overrides the model-decided outcome.
		if r.Router != nil {
			if routed := r.Router(st, sr); routed != "" {
				sr.Outcome = routed
			}
		}

		// Verification gate: concrete issues force a retry_same with feedback
		// (bounded by MaxStepRetries), so an invalid result cannot pass onward.
		if r.Verify != nil && sr.Outcome != OutcomeEscalate {
			if issues := r.Verify(st, sr); len(issues) > 0 {
				st.VerifyNote = strings.Join(issues, "; ")
				sr.Outcome = OutcomeRetrySame
			}
		}

		switch sr.Outcome {
		case OutcomeReroute:
			// Change of approach: continue the outer loop; the next prompt
			// surfaces the reroute marker so the model does not repeat the
			// stuck pattern.
			st.RetryCount = 0
			st.Status = StatusRunning
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventStep, State: st, Text: "step " + strconv.Itoa(st.Step) + " rerouted — change approach"})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "reroute", Status: st.Status, Step: st.Step, Text: st.Summary, Plan: planPtr(st.Plan), Outcome: string(OutcomeReroute)})
			continue
		case OutcomeRetrySame:
			// Retry the same step with feedback, capped by code.
			st.RetryCount++
			st.Status = StatusRunning
			if st.RetryCount > MaxStepRetries {
				st.Status = StatusPartial
				st.Error = fmt.Sprintf("retried %d times without progress", st.RetryCount-1)
				st.Partial = PartialSummaryFor(st)
				_ = r.store().Save(st)
				r.fire(Event{Kind: EventPartial, State: st, Text: st.Partial})
				r.store().AppendEvent(st.ID, LogEvent{Kind: "partial", Status: st.Status, Step: st.Step, Text: st.Partial, Plan: planPtr(st.Plan)})
				return st, fmt.Errorf("goal: %s (partial result saved)", st.Error)
			}
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventStep, State: st, Text: fmt.Sprintf("step %d/%d (retry %d/%d)", st.Step, st.MaxSteps, st.RetryCount, MaxStepRetries)})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "retry_same", Status: st.Status, Step: st.Step, Text: st.Summary, Plan: planPtr(st.Plan), Outcome: string(OutcomeRetrySame)})
			continue
		case OutcomeEscalate:
			// Human gate: persist a durable question and stop blocked.
			// Resume with mow goal resume --answer (milestone 4 wiring).
			st.Status = StatusBlocked
			if st.Question == "" {
				st.Question = "A step escalated: " + strings.TrimSpace(st.Summary)
			}
			st.Error = "waiting for human decision"
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventBlocked, State: st, Text: st.Question})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "blocked", Status: st.Status, Step: st.Step, Text: st.Question, Plan: planPtr(st.Plan), Outcome: string(OutcomeEscalate)})
			return st, fmt.Errorf("goal blocked: %s (resume with --answer)", st.Question)
		case OutcomePartialStop:
			st.Status = StatusPartial
			st.Error = "stopped partial by route"
			st.Partial = PartialSummaryFor(st)
			_ = r.store().Save(st)
			r.fire(Event{Kind: EventPartial, State: st, Text: st.Partial})
			r.store().AppendEvent(st.ID, LogEvent{Kind: "partial", Status: st.Status, Step: st.Step, Text: st.Partial, Plan: planPtr(st.Plan)})
			return st, nil
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

	// Budget exhausted: stop cleanly with a partial result, not a bare
	// failure. The machine-readable line lets hosts show "done 4/6, missing
	// pricing table, best artifact: draft-report.md" instead of an error.
	st.Status = StatusPartial
	st.Error = fmt.Sprintf("max steps %d exceeded", st.MaxSteps)
	st.Partial = PartialSummaryFor(st)
	_ = r.store().Save(st)
	r.fire(Event{Kind: EventPartial, State: st, Text: st.Partial})
	r.store().AppendEvent(st.ID, LogEvent{Kind: "partial", Status: st.Status, Step: st.Step, Text: st.Partial, Plan: planPtr(st.Plan)})
	return st, fmt.Errorf("goal: %s (partial result saved)", st.Error)
}

// partialSummary builds the compact machine-readable partial line: done steps,
// missing steps, and the best artifact (plan items finished or the last reply).
func PartialSummaryFor(st State) string {
	done, missing := 0, 0
	for _, it := range st.Plan.Items {
		if it.Status == "done" {
			done++
		} else {
			missing++
		}
	}
	best := strings.TrimSpace(st.LastReply)
	if best == "" {
		best = strings.TrimSpace(st.Summary)
	}
	if done == 0 && missing == 0 {
		return fmt.Sprintf("stopped at step %d/%d after budget", st.Step, st.MaxSteps)
	}
	line := fmt.Sprintf("done %d/%d items, %d missing", done, done+missing, missing)
	if best != "" {
		if len(best) > 80 {
			best = best[:80] + "…"
		}
		line += " — best: " + best
	}
	return line
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
	if r == nil || r.Engine == nil {
		return
	}
	var eventType mow.EventType
	switch e.Kind {
	case EventStart:
		eventType = mow.EventGoalStart
	case EventStep:
		eventType = mow.EventGoalStep
	case EventDone:
		eventType = mow.EventGoalDone
	case EventFail:
		eventType = mow.EventGoalFail
	case EventPartial:
		eventType = mow.EventGoalPartial
	case EventBlocked:
		eventType = mow.EventGoalBlocked
	default:
		return
	}
	var nodes []mow.GoalNode
	for _, item := range e.State.Plan.Items {
		nodes = append(nodes, mow.GoalNode{
			ID:     item.ID,
			Title:  item.Title,
			Status: string(item.Status),
		})
	}
	r.Engine.Emit(mow.Event{
		Type:       eventType,
		SessionID:  e.State.SessionID,
		Text:       e.Text,
		StopReason: e.State.Error,
		Goal: &mow.GoalEvent{
			ID:       e.State.ID,
			Status:   string(e.State.Status),
			Step:     e.State.Step,
			MaxSteps: e.State.MaxSteps,
			Summary:  e.State.Summary,
			Nodes:    nodes,
		},
	})
}

func stepPrompt(st State) string {
	current := "Current node: " + st.NodeSummary() + "\n\n"
	if st.Step == 0 {
		var b strings.Builder
		b.WriteString(current)
		b.WriteString("Begin work on the goal.\n\nGoal:\n")
		b.WriteString(st.Goal)
		b.WriteString("\n\nIf the goal has multiple parts, first call goal_report status=continue with plan=[...] (id+title+status=pending). ")
		b.WriteString("Otherwise call goal_report status=done summary=… when finished.")
		return b.String()
	}
	var b strings.Builder
	b.WriteString(current)
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
	if facts := st.FactsText(); facts != "" {
		b.WriteString("\n\nDurable evidence so far (use these; do not re-derive):\n")
		b.WriteString(facts)
	}
	if v := strings.TrimSpace(st.VerifyNote); v != "" {
		b.WriteString("\n\nVerification feedback — fix these before proceeding:\n")
		b.WriteString(v)
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

func (r *Runner) workspace() string {
	if r == nil || r.Engine == nil {
		return ""
	}
	return r.Engine.Workspace()
}
