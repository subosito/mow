package goal

import "testing"

func TestContentWithoutMarkers(t *testing.T) {
	in := "Here are bullets:\n- a\n- b\nGOAL_DONE\n"
	got := contentWithoutMarkers(in)
	if got != "Here are bullets:\n- a\n- b" {
		t.Fatalf("%q", got)
	}
	if contentWithoutMarkers("GOAL_DONE") != "" {
		t.Fatal("marker-only should empty")
	}
}

func TestPickSummaryPrefersReport(t *testing.T) {
	got := pickSummary("from report", nil, "GOAL_DONE")
	if got != "from report" {
		t.Fatal(got)
	}
	got = pickSummary("", nil, "real answer\nGOAL_DONE")
	if got != "real answer" {
		t.Fatal(got)
	}
}
