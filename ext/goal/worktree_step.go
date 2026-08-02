package goal

import (
	"context"
	"fmt"
	"strings"
)

// runWorktreeItem executes one plan item inside its own git worktree, commits
// the result, and merges it back into the goal workspace.
//
// Everything about this path is opt-in and degradable. A worktree item running
// without git, without a WorktreeEngineFactory, or in a detached HEAD is not an
// error: it runs as an ordinary step in the goal workspace with a note saying
// why. Isolation is a safety property of the *scheduler*, not a promise the
// goal depends on for correctness.
//
// The merge is the one place a human can be pulled in: a conflict is never
// resolved automatically. The merge aborts (leaving the parent workspace
// clean), the worktree is preserved on disk, and the step escalates.
func (r *Runner) runWorktreeItem(ctx context.Context, exec *Executor, st State, item PlanItem) (StepResult, error) {
	parent := strings.TrimSpace(st.Workspace)
	if parent == "" {
		parent = r.workspace()
	}

	// Preflight. Each failure degrades to a normal step, never a goal failure.
	switch {
	case r.WorktreeEngineFactory == nil:
		return r.fallbackStep(ctx, exec, st, item,
			"no WorktreeEngineFactory configured")
	case !isGitRepo(ctx, parent):
		return r.fallbackStep(ctx, exec, st, item,
			"workspace is not a git repository")
	}

	wt, err := addWorktree(ctx, parent, st.ID, item.ID)
	if err != nil {
		return r.fallbackStep(ctx, exec, st, item,
			"git worktree unavailable: "+truncateRunes(err.Error(), 200))
	}

	eng, err := r.WorktreeEngineFactory(wt.Dir)
	if err != nil {
		wt.cleanup(ctx)
		return StepResult{}, fmt.Errorf("goal: worktree engine for %q: %w", item.ID, err)
	}

	r.fire(Event{Kind: EventStep, State: st,
		Text: fmt.Sprintf("worktree %s → %s (branch %s)", item.ID, wt.Dir, wt.Branch)})

	sub := &Executor{Engine: eng, StoreDir: exec.StoreDir}
	res, runErr := sub.RunStep(ctx, subState(st, item))
	// The sub-step ran against a single-item checklist, so its plan must be
	// mapped back onto the parent's rather than replacing it — otherwise the
	// parent loses every other item and the goal reports done early.
	res.Plan = mergeSubPlan(st.Plan, res.Plan, item)
	if runErr != nil {
		wt.cleanup(ctx)
		return res, runErr
	}

	// A failed step's work is not merged: the branch would carry a known-bad
	// tree into the base. Drop the worktree and report the failure upward
	// unchanged, so Router/Verify see exactly what a normal step would produce.
	if res.Outcome == OutcomeFailed {
		wt.cleanup(ctx)
		res.Summary = appendNote(res.Summary, "worktree discarded (step failed)")
		return res, nil
	}

	committed, err := wt.commitAll(ctx, "mow: "+itemLabel(item))
	if err != nil {
		wt.cleanup(ctx)
		return res, fmt.Errorf("goal: worktree commit for %q: %w", item.ID, err)
	}
	if !committed {
		// Nothing changed on disk: a research/inspection step. Success, no
		// merge, no branch left behind.
		wt.cleanup(ctx)
		res.Summary = appendNote(res.Summary, "worktree: no file changes to merge")
		return res, nil
	}

	mr := wt.merge(ctx)
	switch {
	case mr.Conflicted:
		// Preserve the worktree: a human needs somewhere to resolve this.
		// The conflict leads the summary because the runner derives the
		// human-facing question from it.
		res.Outcome = OutcomeEscalate
		res.Summary = fmt.Sprintf(
			"merge conflict in %s — worktree kept at %s (branch %s); resolve and merge, or reject",
			item.ID, wt.Dir, wt.Branch) + "\n" + strings.TrimSpace(res.Summary)
		res.Text = strings.TrimSpace(res.Text + "\n\n" + mergeDiffText(mr.Diff))
		return res, nil
	case mr.Err != nil:
		wt.cleanup(ctx)
		return res, fmt.Errorf("goal: worktree merge for %q: %w", item.ID, mr.Err)
	}

	wt.cleanup(ctx)
	res.Summary = appendNote(res.Summary, "merged "+wt.Branch+" into "+wt.Base)
	if diff := mergeDiffText(mr.Diff); diff != "" {
		res.Text = strings.TrimSpace(res.Text + "\n\n" + diff)
	}
	return res, nil
}

// fallbackStep runs the item as an ordinary step and records why isolation was
// declined, so a run that silently lost its worktrees is still diagnosable.
func (r *Runner) fallbackStep(ctx context.Context, exec *Executor, st State, item PlanItem, why string) (StepResult, error) {
	note := "worktree isolation skipped (" + why + ")"
	r.fire(Event{Kind: EventStep, State: st, Text: note})
	res, err := exec.RunStep(ctx, subState(st, item))
	res.Plan = mergeSubPlan(st.Plan, res.Plan, item)
	if err != nil {
		return res, err
	}
	res.Summary = appendNote(res.Summary, note)
	return res, nil
}

// mergeSubPlan projects a single-item sub-step's checklist back onto the
// parent plan. The sub-step only ever sees its own item, so its plan is a
// status update for that item, never a replacement checklist.
func mergeSubPlan(parent, sub Plan, item PlanItem) Plan {
	if !parent.HasItems() {
		return sub
	}
	out := Plan{Items: append([]PlanItem(nil), parent.Items...)}
	for _, sit := range sub.Items {
		if sit.Status == ItemPending || sit.Status == "" {
			continue
		}
		setParentItem(&out, sit.ID, sit.Status, sit.Note)
	}
	return out
}

// mergeDiffText renders the bounded diff summary attached to a step result.
func mergeDiffText(diff string) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return ""
	}
	return "merge diff:\n" + truncateRunes(diff, maxMergeDiffChars)
}

// appendNote adds one bracketed note to a summary line.
func appendNote(summary, note string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "[" + note + "]"
	}
	return summary + "\n[" + note + "]"
}

// itemLabel is the human-readable commit subject for an item.
func itemLabel(item PlanItem) string {
	if t := strings.TrimSpace(item.Title); t != "" {
		return truncateRunes(t, 72)
	}
	return strings.TrimSpace(item.ID)
}
