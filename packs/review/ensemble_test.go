package review

import (
	"context"
	"sync"
	"testing"
	"time"
)

type ensembleFake struct {
	model   string
	replies []string
	wait    <-chan struct{}

	mu    sync.Mutex
	calls int
}

func (f *ensembleFake) Ask(ctx context.Context, _, _ string) (string, error) {
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls > len(f.replies) {
		return "", context.Canceled
	}
	return f.replies[f.calls-1], nil
}
func (f *ensembleFake) Model() string { return f.model }

func TestEnsembleMergesCandidatesAndUsesFirstMemberForVerification(t *testing.T) {
	first := &ensembleFake{model: "first", replies: []string{
		`{"findings":[{"title":"first","category":"correctness","severity":"high","confidence":"high","path":"internal/api/users.go","start_line":1,"evidence":"e","impact":"i","recommendation":"r"}]}`,
		`{"verdicts":[{"id":"review-001","status":"confirmed"},{"id":"review-002","status":"confirmed"}]}`,
	}}
	second := &ensembleFake{model: "second", replies: []string{
		`{"findings":[{"title":"second","category":"correctness","severity":"medium","confidence":"high","path":"internal/db/query.go","start_line":1,"evidence":"e","impact":"i","recommendation":"r"}]}`,
	}}
	ens, err := NewEnsembleReviewer([]EnsembleMember{{Name: "alpha", Reviewer: first}, {Name: "beta", Reviewer: second}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), ens, testScope(t), Request{Profile: GeneralProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Report.Findings) != 2 {
		t.Fatalf("findings = %+v", res.Report.Findings)
	}
	for _, finding := range res.Report.Findings {
		if finding.Extra["reviewer"] == "" {
			t.Errorf("finding %q missing reviewer provenance: %+v", finding.Title, finding.Extra)
		}
	}
	second.mu.Lock()
	secondCalls := second.calls
	second.mu.Unlock()
	if secondCalls != 1 {
		t.Fatalf("second member calls = %d, want candidate pass only", secondCalls)
	}
}

func TestEnsembleBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	members := make([]EnsembleMember, 3)
	for i := range members {
		members[i] = EnsembleMember{Name: string(rune('a' + i)), Reviewer: &startedReviewer{started: started, release: release}}
	}
	ens, err := NewEnsembleReviewer(members, 2)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ens.Ask(context.Background(), "", ""); done <- err }()
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("started more than maxParallel reviewers")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type startedReviewer struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (r *startedReviewer) Ask(ctx context.Context, _, _ string) (string, error) {
	r.started <- struct{}{}
	select {
	case <-r.release:
		return `{"findings":[]}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (r *startedReviewer) Model() string { return "fake" }
