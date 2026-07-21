package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/subosito/mow"
)

// Active finish signal for the in-flight goal step (set via tool goal_report).
type finishSignal struct {
	mu      sync.Mutex
	done    bool
	failed  bool
	cont    bool // explicit continue (progress / plan update without finishing goal)
	reason  string
	summary string
	// plan mutations applied on the signal (copied into StepResult).
	plan Plan
	// planTouched is true if plan or an item was updated this step.
	planTouched bool
	// rejectDone is set when status=done but checklist not complete.
	rejectDone string
}

func (f *finishSignal) report(status, reason, summary string, plan []PlanItem, itemID, itemStatus, itemNote string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := strings.TrimSpace(summary); s != "" {
		f.summary = s
	}
	// Apply plan set/replace first.
	if len(plan) > 0 {
		if err := f.plan.ReplaceItems(plan); err == nil {
			f.planTouched = true
		}
	}
	// Then item update.
	if id := strings.TrimSpace(itemID); id != "" {
		st := strings.TrimSpace(itemStatus)
		if st == "" {
			st = "done"
		}
		if err := f.plan.SetItem(id, st, itemNote); err == nil {
			f.planTouched = true
		}
	}

	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "complete", "success":
		// Checklist gate: if plan exists, all items must be done/skipped.
		if f.plan.HasItems() && !f.plan.AllDone() {
			f.rejectDone = "checklist incomplete — mark remaining items done or use status=continue"
			f.done = false
			f.failed = false
			f.cont = true
			return
		}
		f.done = true
		f.failed = false
		f.cont = false
		f.reason = ""
		f.rejectDone = ""
	case "failed", "fail", "blocked", "error":
		f.failed = true
		f.done = false
		f.cont = false
		f.reason = strings.TrimSpace(reason)
		f.rejectDone = ""
	case "continue", "progress", "next", "":
		// Empty status with plan/item update counts as continue.
		f.cont = true
		f.done = false
		f.failed = false
		f.rejectDone = ""
	}
}

func (f *finishSignal) outcome() (done, failed, cont bool, reason, summary string, plan Plan, reject string, touched bool) {
	if f == nil {
		return false, false, false, "", "", Plan{}, "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copy plan items for the caller.
	var pc Plan
	if len(f.plan.Items) > 0 {
		pc.Items = append([]PlanItem(nil), f.plan.Items...)
	}
	return f.done, f.failed, f.cont || f.planTouched, f.reason, f.summary, pc, f.rejectDone, f.planTouched
}

func (f *finishSignal) seedPlan(p Plan) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Deep-ish copy items.
	if p.HasItems() {
		f.plan.Items = append([]PlanItem(nil), p.Items...)
	}
}

type finishCtxKey struct{}

func withFinish(ctx context.Context, f *finishSignal) context.Context {
	return context.WithValue(ctx, finishCtxKey{}, f)
}

func finishFrom(ctx context.Context) *finishSignal {
	v, _ := ctx.Value(finishCtxKey{}).(*finishSignal)
	return v
}

// reportTool lets the model declare goal completion / plan updates.
// Injected only for goal steps via PromptOpts.ExtraTools.
type reportTool struct{}

// ReportTool is the goal_report tool for PromptOpts.ExtraTools.
func ReportTool() mow.Tool { return reportTool{} }

func (reportTool) Name() string { return "goal_report" }
func (reportTool) Description() string {
	return "Report progress or completion for the CURRENT outer-loop goal. " +
		"status=continue: update checklist / progress (set plan=[{id,title,status}] once, " +
		"or item_id+item_status to mark one item). " +
		"status=done: finish the whole goal (requires summary; if a checklist exists all items must be done/skipped). " +
		"status=failed: fail the goal with reason. " +
		"Stops the tool loop for this step."
}
func (reportTool) ReadOnly() bool { return true }
func (reportTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "status":{"type":"string","enum":["done","failed","continue"],"description":"done=goal complete; failed=goal blocked; continue=progress/plan update"},
    "summary":{"type":"string","description":"User-facing result (required when status=done)"},
    "reason":{"type":"string","description":"Failure reason when status=failed"},
    "plan":{"type":"array","description":"Set/replace checklist (use once early). Items: id, title, status",
      "items":{"type":"object","properties":{
        "id":{"type":"string"},
        "title":{"type":"string"},
        "status":{"type":"string","enum":["pending","done","failed","skipped"]}
      }}},
    "item_id":{"type":"string","description":"Mark one checklist item"},
    "item_status":{"type":"string","enum":["pending","done","failed","skipped"]},
    "item_note":{"type":"string"}
  },
  "required":["status"]
}`)
}
func (reportTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Status     string     `json:"status"`
		Reason     string     `json:"reason"`
		Summary    string     `json:"summary"`
		Plan       []PlanItem `json:"plan"`
		ItemID     string     `json:"item_id"`
		ItemStatus string     `json:"item_status"`
		ItemNote   string     `json:"item_note"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	sig := finishFrom(ctx)
	if sig == nil {
		return "goal_report ignored (no active outer-loop goal)", nil
	}
	sig.report(a.Status, a.Reason, a.Summary, a.Plan, a.ItemID, a.ItemStatus, a.ItemNote)
	done, failed, cont, _, _, plan, reject, _ := sig.outcome()
	if reject != "" {
		return "goal_report: " + reject + "\nchecklist:\n" + plan.Format(), mow.ErrAgentDone
	}
	var msg string
	switch {
	case done:
		if strings.TrimSpace(a.Summary) == "" {
			msg = "recorded: goal done (warning: pass summary= with the result text for the user)"
		} else {
			msg = "recorded: goal done"
		}
	case failed:
		msg = fmt.Sprintf("recorded: goal failed (%s)", strings.TrimSpace(a.Reason))
	case cont || len(a.Plan) > 0 || strings.TrimSpace(a.ItemID) != "":
		msg = "recorded: progress"
		if plan.HasItems() {
			msg += "\nchecklist:\n" + plan.Format()
		}
	default:
		return fmt.Sprintf("goal_report: unknown status %q (use done|failed|continue)", a.Status), nil
	}
	return msg, mow.ErrAgentDone
}

// ParseStatusJSON extracts ```goal-status {json} ``` blocks from assistant text.
func ParseStatusJSON(text string) (done, failed bool, reason, summary string) {
	const fence = "```goal-status"
	for {
		i := strings.Index(text, fence)
		if i < 0 {
			return
		}
		rest := text[i+len(fence):]
		rest = strings.TrimLeft(rest, " \t\r\n")
		end := strings.Index(rest, "```")
		if end < 0 {
			return
		}
		body := strings.TrimSpace(rest[:end])
		var obj struct {
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(body), &obj) == nil {
			switch strings.ToLower(strings.TrimSpace(obj.Status)) {
			case "done", "complete", "success":
				return true, false, "", strings.TrimSpace(obj.Summary)
			case "failed", "fail", "blocked", "error":
				return false, true, strings.TrimSpace(obj.Reason), strings.TrimSpace(obj.Summary)
			}
		}
		text = rest[end+3:]
	}
}
