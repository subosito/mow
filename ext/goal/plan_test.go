package goal

import "testing"

func TestPlanReplaceAndSetItem(t *testing.T) {
	var p Plan
	if err := p.ReplaceItems([]PlanItem{
		{Title: "First task"},
		{ID: "b", Title: "Second", Status: ItemPending},
	}); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.Items[0].ID == "" {
		t.Fatalf("%+v", p.Items)
	}
	if err := p.SetItem(p.Items[0].ID, "done", "ok"); err != nil {
		t.Fatal(err)
	}
	if p.AllDone() {
		t.Fatal("second still pending")
	}
	if err := p.SetItem("b", "done", ""); err != nil {
		t.Fatal(err)
	}
	if !p.AllDone() {
		t.Fatal("want all done")
	}
}

func TestPlanDoneGate(t *testing.T) {
	p := Plan{Items: []PlanItem{{ID: "a", Title: "A", Status: ItemPending}}}
	if p.AllDone() {
		t.Fatal("pending not done")
	}
	p.Items[0].Status = ItemDone
	if !p.AllDone() {
		t.Fatal("want done")
	}
}
