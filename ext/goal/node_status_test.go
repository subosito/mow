package goal

import (
	"strings"
	"testing"
)

func TestNodeStatusAndSummary(t *testing.T) {
	st := State{
		Goal: "verify pricing", Status: StatusRunning, CurrentItem: "b",
		Plan: Plan{Items: []PlanItem{
			{ID: "a", Title: "collect prices", Status: ItemDone},
			{ID: "b", Title: "verify-pricing", Status: ItemPending},
			{ID: "c", Title: "publish", Status: ItemFailed},
		}},
		Facts: []Fact{{Claim: "one"}, {Claim: "two"}, {Claim: "three"}},
	}
	nodes := st.Nodes()
	if len(nodes) != 3 || nodes[0] != (NodeStatus{ID: "a", Title: "collect prices", Status: "done"}) ||
		nodes[2].Status != "failed" {
		t.Fatalf("nodes=%+v", nodes)
	}
	if got, want := st.NodeSummary(), "node 2/3 [b] verify-pricing (evidence: 3)"; got != want {
		t.Fatalf("NodeSummary=%q want %q", got, want)
	}
}

func TestNodeSummarySelectsPendingAndHandlesNoPlan(t *testing.T) {
	mixed := State{Goal: "ship", Plan: Plan{Items: []PlanItem{
		{ID: "a", Title: "done", Status: ItemDone},
		{ID: "b", Title: "next", Status: ItemPending},
	}}}
	if got := mixed.NodeSummary(); got != "node 2/2 [b] next (evidence: 0)" {
		t.Fatalf("mixed summary=%q", got)
	}
	empty := State{Goal: "small goal", Status: StatusRunning}
	if got := empty.NodeSummary(); got != "node 1/1 [goal] small goal (evidence: 0)" {
		t.Fatalf("empty summary=%q", got)
	}
	nodes := empty.Nodes()
	if len(nodes) != 1 || nodes[0].ID != "goal" || nodes[0].Status != "running" {
		t.Fatalf("empty nodes=%+v", nodes)
	}
}

func TestSystemAppendIsStepInvariantAndStepPromptCarriesState(t *testing.T) {
	// The system prompt is the head of the provider's cached prefix, and each
	// goal step replays the whole conversation after it. Anything that varies
	// by step there re-bills that history uncached, so SystemAppend may depend
	// only on the goal text. Per-step state belongs in stepPrompt.
	plan := func(a, b PlanItemStatus) Plan {
		return Plan{Items: []PlanItem{
			{ID: "a", Title: "prepare", Status: a},
			{ID: "b", Title: "verify", Status: b},
		}}
	}
	steps := []State{
		{Goal: "ship", Step: 0, MaxSteps: 4},
		{Goal: "ship", Step: 1, MaxSteps: 4, Plan: plan(ItemPending, ItemPending)},
		{Goal: "ship", Step: 2, MaxSteps: 4, CurrentItem: "b", Plan: plan(ItemDone, ItemPending),
			Facts: []Fact{{Claim: "tests pass"}}, Summary: "prepared", VerifyNote: "check b"},
		{Goal: "ship", Step: 3, MaxSteps: 4, Plan: plan(ItemDone, ItemDone)},
	}
	want := SystemAppend(steps[0])
	for i, st := range steps[1:] {
		if got := SystemAppend(st); got != want {
			t.Fatalf("SystemAppend changed at step %d:\n--- step 0\n%s\n--- step %d\n%s", i+1, want, i+1, got)
		}
	}
	for _, leaked := range []string{"Current node:", "step 1 of", "- [pending]", "Focus this step on", "(evidence:"} {
		if strings.Contains(want, leaked) {
			t.Errorf("SystemAppend carries per-step state %q:\n%s", leaked, want)
		}
	}
	// The stable protocol survives in generic form.
	for _, keep := range []string{"ship", "goal_report status=done", "item_id=<id>", "plan=[", "Do not discard uncommitted work"} {
		if !strings.Contains(want, keep) {
			t.Errorf("SystemAppend lost protocol %q:\n%s", keep, want)
		}
	}
	// A different goal is a different system prompt: invariance is per goal.
	if SystemAppend(State{Goal: "other"}) == want {
		t.Fatal("SystemAppend ignores the goal text")
	}

	mid := stepPrompt(steps[2])
	for _, keep := range []string{
		"Current node: node 2/2 [b] verify (evidence: 1)\n\n",
		"Outer loop step 3 of 4.",
		"Checklist:\n", "- [done] prepare (a)", "- [pending] verify (b)",
		"Focus: [b] verify", "item_id=b item_status=done",
		"tests pass", "check b", "prepared",
	} {
		if !strings.Contains(mid, keep) {
			t.Errorf("stepPrompt missing %q:\n%s", keep, mid)
		}
	}
	if !strings.HasPrefix(mid, "Current node: ") {
		t.Errorf("stepPrompt must lead with the node summary:\n%s", mid)
	}
	noPlan := stepPrompt(State{Goal: "ship", Step: 1, MaxSteps: 4})
	if !strings.Contains(noPlan, "No checklist yet.") || !strings.Contains(noPlan, "plan=[...]") {
		t.Errorf("later step without a plan lost the planning hint:\n%s", noPlan)
	}
	if !strings.Contains(stepPrompt(steps[3]), "All items done") {
		t.Errorf("all-done step lost its finish hint")
	}
}

func TestFirstStepPromptHasSyntheticNode(t *testing.T) {
	prompt := stepPrompt(State{Goal: "tiny", MaxSteps: 1})
	if !strings.HasPrefix(prompt, "Current node: node 1/1 [goal] tiny (evidence: 0)\n\n") {
		t.Fatalf("prompt=%q", prompt)
	}
	if !strings.Contains(prompt, "goal_report status=continue with plan=") {
		t.Fatalf("first-step planning protocol lost: %q", prompt)
	}
}
