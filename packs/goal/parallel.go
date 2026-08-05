package goal

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MaxParallelWidth caps Spec.ParallelMax: more concurrent engines than this
// buys nothing and multiplies upstream rate-limit pressure.
const MaxParallelWidth = 8

// parallelWidth returns how many independent plan items this step may run
// concurrently: 0/1 means "run the normal sequential step".
//
// Parallel steps are opt-in and require an engine source, because
// mow.Engine serializes Prompt calls (promptMu) — one engine cannot run two
// steps at once. With no Runner.EngineFactory the runner stays sequential.
func (r *Runner) parallelWidth(st State) int {
	if r == nil || r.EngineFactory == nil {
		return 0
	}
	want := st.ParallelMax
	if want < 2 {
		return 0
	}
	if want > MaxParallelWidth {
		want = MaxParallelWidth
	}
	pending := 0
	for _, it := range st.Plan.Items {
		if it.Status == ItemPending || it.Status == "" {
			pending++
		}
	}
	if pending < 2 {
		return 0
	}
	if want > pending {
		want = pending
	}
	return want
}

// pendingBatch returns up to n pending plan items in order.
func pendingBatch(p Plan, n int) []PlanItem {
	var out []PlanItem
	for _, it := range p.Items {
		if it.Status == ItemPending || it.Status == "" {
			out = append(out, it)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// subState focuses a copy of the parent state on one plan item: same goal,
// facts and budget, but a single-item checklist so the sub-step works on
// exactly that node.
func subState(st State, item PlanItem) State {
	sub := st
	sub.Facts = append([]Fact(nil), st.Facts...)
	it := item
	it.Status = ItemPending
	sub.Plan = Plan{Items: []PlanItem{it}}
	sub.CurrentItem = it.ID
	sub.VerifyNote = ""
	sub.Partial = ""
	sub.Question = ""
	sub.Error = ""
	return sub
}

// runParallelStep runs up to width pending plan items as concurrent sub-steps,
// each on its own Engine (from Runner.EngineFactory) and its own finishSignal,
// then joins the results into ONE StepResult so the caller's outcome handling
// (Router, Verify, reroute/retry/escalate/partial) is unchanged.
func (r *Runner) runParallelStep(ctx context.Context, exec *Executor, st State, width int) (StepResult, error) {
	items := pendingBatch(st.Plan, width)
	if len(items) < 2 {
		return exec.RunStep(ctx, st)
	}

	out := make([]parallelOutcome, len(items))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(i int, item PlanItem) {
			defer wg.Done()
			out[i].Item = item
			eng, err := r.EngineFactory()
			if err != nil {
				out[i].Err = fmt.Errorf("goal: parallel engine: %w", err)
				cancel()
				return
			}
			defer eng.Close()
			// Each sub-step gets its own Executor+Engine; RunStep builds its
			// own finishSignal and stores it in its own derived context, so
			// the signal is per-goroutine (never shared across sub-steps).
			sub := &Executor{Engine: eng, StoreDir: exec.StoreDir}
			var res StepResult
			if isWorktreeItem(item) {
				// Worktree items bring their own engine (rooted in the
				// checkout), so the factory engine above is unused here.
				res, err = r.runWorktreeItem(runCtx, sub, st, item)
			} else {
				res, err = sub.RunStep(runCtx, subState(st, item))
			}
			out[i].Res, out[i].Err = res, err
			if err != nil {
				cancel() // fail fast: siblings stop at the next tool boundary
			}
		}(i, item)
	}
	wg.Wait()

	return joinParallel(st, out), firstParallelErr(out)
}

// parallelOutcome is one joined sub-step (item + result + error).
type parallelOutcome struct {
	Item PlanItem
	Res  StepResult
	Err  error
}

func firstParallelErr(subs []parallelOutcome) error {
	for _, s := range subs {
		if s.Err != nil {
			return s.Err
		}
	}
	return nil
}

// joinParallel merges sub-step results into the parent step result:
//   - plan: item statuses from each sub-step land on the parent checklist
//   - evidence: concatenated (parent runner dedupes by claim as usual)
//   - summary: combined, one line per item
//   - outcome: any failure wins; otherwise continue (or done when the whole
//     checklist finished and a sub-step declared the goal done)
func joinParallel(st State, subs []parallelOutcome) StepResult {
	merged := StepResult{Plan: st.Plan}
	if merged.Plan.HasItems() {
		merged.Plan.Items = append([]PlanItem(nil), st.Plan.Items...)
	}
	var (
		lines     []string
		texts     []string
		failed    bool
		escalated bool
		reason    string
		question  string
		anyDone   bool
		sessedID  string
	)
	for _, s := range subs {
		merged.Usage.InputTokens += s.Res.Usage.InputTokens
		merged.Usage.OutputTokens += s.Res.Usage.OutputTokens
		if sessedID == "" {
			sessedID = s.Res.SessionID
		}
		merged.Evidence = append(merged.Evidence, s.Res.Evidence...)

		// Apply the sub-step's item statuses onto the parent plan.
		applied := false
		for _, sit := range s.Res.Plan.Items {
			if sit.Status == ItemPending || sit.Status == "" {
				continue
			}
			if setParentItem(&merged.Plan, sit.ID, sit.Status, sit.Note) {
				applied = true
			}
		}
		switch s.Res.Outcome {
		case OutcomeFailed:
			failed = true
			if reason == "" {
				reason = strings.TrimSpace(s.Res.Reason)
				if reason == "" {
					reason = "parallel item " + s.Item.ID + " failed"
				}
			}
			setParentItem(&merged.Plan, s.Item.ID, ItemFailed, s.Res.Reason)
		case OutcomeDone:
			anyDone = true
			if !applied {
				setParentItem(&merged.Plan, s.Item.ID, ItemDone, "")
			}
		case OutcomeEscalate:
			// A sub-step that needs a human (e.g. a worktree merge conflict)
			// must not be flattened into "continue": the parent has to block.
			// The item stays pending so resuming retries it after the human
			// resolves the conflict.
			escalated = true
			if question == "" {
				question = strings.TrimSpace(s.Res.Summary)
				if question == "" {
					question = "parallel item " + s.Item.ID + " escalated"
				}
			}
		}
		if s.Err != nil {
			continue
		}
		if sum := strings.TrimSpace(s.Res.Summary); sum != "" {
			lines = append(lines, "["+s.Item.ID+"] "+sum)
		}
		if t := strings.TrimSpace(s.Res.Text); t != "" {
			texts = append(texts, t)
		}
	}
	merged.SessionID = sessedID
	merged.Summary = truncateRunes(strings.Join(lines, "\n"), 2000)
	merged.Text = strings.Join(texts, "\n\n")

	switch {
	case failed:
		merged.Outcome = OutcomeFailed
		merged.Reason = reason
	case escalated:
		// Escalation outranks "continue": a human decision is pending.
		// Failure still outranks escalation (a broken item is the bigger news).
		merged.Outcome = OutcomeEscalate
		merged.Summary = truncateRunes(strings.TrimSpace(question+"\n"+merged.Summary), 2000)
	case anyDone && merged.Plan.AllDone():
		merged.Outcome = OutcomeDone
	default:
		merged.Outcome = OutcomeContinue
	}
	return merged
}

// setParentItem marks one item on the parent plan; reports whether it existed.
func setParentItem(p *Plan, id string, status PlanItemStatus, note string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for i := range p.Items {
		if p.Items[i].ID != id {
			continue
		}
		p.Items[i].Status = status
		if n := strings.TrimSpace(note); n != "" {
			p.Items[i].Note = n
		}
		return true
	}
	return false
}
