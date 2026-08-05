package goal

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecentFactsWorkspaceMergedDedupedBounded(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	ws := filepath.Join(t.TempDir(), "project")
	old := State{ID: "old", Workspace: ws, Facts: []Fact{{Claim: "shared", Source: "old"}, {Claim: "old only"}}}
	if err := s.Save(old); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	newer := State{ID: "new", Workspace: ws, Facts: []Fact{{Claim: "new only"}, {Claim: "Shared", Source: "new"}}}
	if err := s.Save(newer); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(State{ID: "other", Workspace: t.TempDir(), Facts: []Fact{{Claim: "foreign"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecentFacts(ws, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Claim != "Shared" || got[0].Source != "new" || got[1].Claim != "new only" || got[2].Claim != "old only" {
		t.Fatalf("facts=%+v", got)
	}
	bounded, err := s.RecentFacts(ws, 1)
	if err != nil || len(bounded) != 1 {
		t.Fatalf("bounded=%+v err=%v", bounded, err)
	}
}
