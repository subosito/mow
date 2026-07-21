package goal

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow"
)

// Executor runs one outer-loop step via Engine.PromptWith.
// Runner owns durable state; Executor is the single unit of LLM+tool work.
type Executor struct {
	Engine *mow.Engine
	// StoreDir is the goals root (for process tools). Empty → DefaultDir().
	StoreDir string
}

// RunStep executes one Prompt for the current goal state and returns a StepResult.
func (e *Executor) RunStep(ctx context.Context, st State) (StepResult, error) {
	if e == nil || e.Engine == nil {
		return StepResult{}, fmt.Errorf("goal: nil executor engine")
	}
	sig := &finishSignal{}
	sig.seedPlan(st.Plan)

	storeDir := strings.TrimSpace(e.StoreDir)
	if storeDir == "" {
		storeDir = DefaultDir()
	}
	stepCtx := withFinish(ctx, sig)
	stepCtx = withProcessScope(stepCtx, processScope{GoalID: st.ID, Root: storeDir})

	tools := []mow.Tool{ReportTool()}
	tools = append(tools, ProcessTools()...)

	res, err := e.Engine.PromptWith(stepCtx, stepPrompt(st), mow.PromptOpts{
		SystemAppend: SystemAppend(st),
		ExtraTools:   tools,
	})

	out := StepResult{
		Text:      res.Text,
		SessionID: res.SessionID,
		Usage:     res.Usage,
		Plan:      st.Plan,
	}

	done, failed, cont, reason, summary, plan, reject, touched := sig.outcome()
	if touched || plan.HasItems() {
		out.Plan = plan
	}
	if summary != "" {
		out.Summary = summary
	}

	// Prompt-level errors (max turns / network) — surface to Runner; still pass plan.
	if err != nil {
		return out, err
	}

	if reject != "" {
		out.Outcome = OutcomeContinue
		if out.Summary == "" {
			out.Summary = reject
		}
		return out, nil
	}
	if done {
		out.Outcome = OutcomeDone
		if out.Summary == "" {
			out.Summary = pickSummary(summary, e.Engine, res.Text)
		}
		return out, nil
	}
	if failed {
		out.Outcome = OutcomeFailed
		out.Reason = reason
		if out.Summary == "" {
			out.Summary = pickSummary(summary, e.Engine, res.Text)
		}
		return out, nil
	}
	if cont || touched {
		out.Outcome = OutcomeContinue
		if out.Summary == "" {
			out.Summary = pickSummary(summary, e.Engine, res.Text)
		}
		return out, nil
	}

	// Text markers / status fences when tool not used.
	tdone, tfailed, treason, treport := resolveOutcomeText(res.Text)
	out.Summary = pickSummary(treport, e.Engine, res.Text)
	if tdone {
		if out.Plan.HasItems() && !out.Plan.AllDone() {
			out.Outcome = OutcomeContinue
			if out.Summary == "" {
				out.Summary = "checklist incomplete — cannot finish yet"
			}
			return out, nil
		}
		out.Outcome = OutcomeDone
		return out, nil
	}
	if tfailed {
		out.Outcome = OutcomeFailed
		out.Reason = treason
		return out, nil
	}
	out.Outcome = OutcomeContinue
	return out, nil
}

func resolveOutcomeText(text string) (done, failed bool, reason, summary string) {
	if d, f, r, s := ParseStatusJSON(text); d || f {
		return d, f, r, s
	}
	d, f, r := ParseOutcome(text)
	return d, f, r, ""
}
