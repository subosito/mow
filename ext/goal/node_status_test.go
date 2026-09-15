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

func TestStepPromptsInjectNodeAndKeepChecklist(t *testing.T) {
	st := State{Goal: "ship", Step: 1, MaxSteps: 4, CurrentItem: "b", Plan: Plan{Items: []PlanItem{
		{ID: "a", Title: "prepare", Status: ItemDone},
		{ID: "b", Title: "verify", Status: ItemPending},
	}}}
	wantNode := "Current node: node 2/2 [b] verify (evidence: 0)"
	for name, prompt := range map[string]string{"step": stepPrompt(st), "system": SystemAppend(st)} {
		if !strings.HasPrefix(prompt, wantNode+"\n\n") {
			t.Errorf("%s missing leading node summary:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, "Checklist:\n") || !strings.Contains(prompt, "- [pending] verify (b)") {
			t.Errorf("%s lost full checklist protocol:\n%s", name, prompt)
		}
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
