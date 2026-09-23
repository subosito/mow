package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/subosito/mow"
)

type planTool struct {
	plan    Plan
	summary string
}

func (planTool) Name() string { return "plan_submit" }
func (planTool) Description() string {
	return "Submit the read-only implementation plan. Provide 1-64 ordered pending checklist items with stable ids/titles and optional worktree worker, plus a concise summary. This stores a pending goal but does not execute it."
}
func (planTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["items","summary"],"properties":{"summary":{"type":"string","maxLength":4000},"items":{"type":"array","minItems":1,"maxItems":64,"items":{"type":"object","additionalProperties":false,"required":["id","title"],"properties":{"id":{"type":"string"},"title":{"type":"string"},"worker":{"type":"string","enum":["","worktree"]}}}}}}`)
}
func (t *planTool) Exec(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Summary string     `json:"summary"`
		Items   []PlanItem `json:"items"`
	}
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", err
	}
	for i := range in.Items {
		in.Items[i].Status = ItemPending
	}
	var plan Plan
	if err := plan.ReplaceItems(in.Items); err != nil {
		return "", err
	}
	if !plan.HasItems() {
		return "", fmt.Errorf("plan_submit: at least one item required")
	}
	t.plan = plan
	t.summary = truncateRunes(strings.TrimSpace(in.Summary), 4000)
	return "plan recorded; stop and present it to the user", mow.ErrAgentDone
}
func (planTool) ReadOnly() bool    { return true }
func (planTool) Sequential() bool  { return true }
func (planTool) Exploratory() bool { return false }

// Plan creates and persists a pending goal from one read-only planning Prompt.
// It never executes the resulting checklist.
func (r *Runner) Plan(ctx context.Context, spec Spec) (State, error) {
	if r == nil || r.Engine == nil {
		return State{}, fmt.Errorf("goal plan: nil engine")
	}
	spec, err := NormalizeSpec(spec)
	if err != nil {
		return State{}, err
	}
	tool := &planTool{}
	prompt := "Create an implementation plan only; do not edit files or run shell. Inspect the repository with read-only tools as needed. Define acceptance/verification as checklist items. Call plan_submit exactly once when ready. Objective:\n" + spec.Goal
	res, err := r.Engine.PromptWith(ctx, prompt, mow.PromptOpts{ReadOnly: true, AllowedTools: []string{"read", "glob", "grep", "delegate", "plan_submit"}, ExtraTools: []mow.Tool{tool}, SystemAppend: "Planning mode is read-only. Return an ordered, bounded plan; do not implement it."})
	if err != nil {
		return State{}, err
	}
	if !tool.plan.HasItems() {
		return State{}, fmt.Errorf("goal plan: model did not submit a plan")
	}
	st, err := r.Create(spec)
	if err != nil {
		return State{}, err
	}
	st.Plan = tool.plan
	st.Summary = tool.summary
	if st.Summary == "" {
		st.Summary = strings.TrimSpace(res.Text)
	}
	st.SetPhase("planned")
	if err := r.save(st); err != nil {
		return State{}, err
	}
	r.fire(Event{Kind: EventStep, State: st, Text: "plan ready; run explicitly with /goal run " + st.ID})
	return st, nil
}
