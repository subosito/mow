package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCandidatesRejectsOversizedReply(t *testing.T) {
	_, err := parseCandidates(strings.Repeat("x", maxModelReplyBytes+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCandidatesRejectsTooManyFindings(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"findings":[`)
	for i := 0; i < maxCandidateFindings+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"title":"t%d","severity":"high","confidence":"high","category":"correctness","path":"a.go","start_line":1,"evidence":"e"}`, i)
	}
	b.WriteString(`]}`)
	_, err := parseCandidates(b.String())
	if err == nil || !strings.Contains(err.Error(), "findings") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppendReportNoteCapsNotes(t *testing.T) {
	rep := NewReport("general")
	for i := 0; i < maxReportNotes+10; i++ {
		appendReportNote(rep, fmt.Sprintf("note %d", i))
	}
	if len(rep.Notes) != maxReportNotes {
		t.Fatalf("notes = %d want %d", len(rep.Notes), maxReportNotes)
	}
}

func TestResolveScopeAllowsSymlinkWorkspace(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "ws")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: link, Paths: []string{"main.go"}, Budget: "small",
	})
	if err != nil {
		t.Fatalf("symlink workspace should be allowed: %v", err)
	}
	if !strings.Contains(strings.Join(sc.Paths(), " "), "main.go") {
		t.Fatalf("paths=%v", sc.Paths())
	}
}

func TestReadWorkspaceFileCapsHugeFile(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "big.go")
	// Don't write 8MiB+; stub size via a regular small file and the helper's
	// open path — use a file just over the cap.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxScopeFileRead + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, err = readWorkspaceFile(ws, "big.go")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveScopeRejectsSymlinkPath(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	secret := filepath.Join(target, "secret.go")
	if err := os.WriteFile(secret, []byte("package secret\n\nfunc Secret() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	_, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: root, Paths: []string{"link.go"}, Budget: "small",
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveScopeSkipsSymlinkInTree(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	secret := filepath.Join(target, "secret.go")
	if err := os.WriteFile(secret, []byte("package secret\n\nfunc Secret() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(pkg, "real.go")
	if err := os.WriteFile(real, []byte("package pkg\n\nfunc Real() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pkg, "link.go")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: root, Paths: []string{"pkg"}, Budget: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sc.Paths(), " ")
	if !strings.Contains(got, "pkg/real.go") {
		t.Fatalf("real file missing: %v", sc.Paths())
	}
	if strings.Contains(got, "link.go") {
		t.Fatalf("symlink file must not be in scope: %v", sc.Paths())
	}
}

func TestExpandPathsCapsCandidates(t *testing.T) {
	prev := maxExpandCandidates
	maxExpandCandidates = 4
	t.Cleanup(func() { maxExpandCandidates = prev })

	root := t.TempDir()
	for i := 0; i < 8; i++ {
		name := filepath.Join(root, fmt.Sprintf("f%d.go", i))
		if err := os.WriteFile(name, []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: root, Paths: []string{"."}, Budget: "large",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Truncated {
		t.Fatal("expected truncated scope when walk hits candidate cap")
	}
	if len(sc.Paths()) > 4 {
		t.Fatalf("paths=%d want <=4", len(sc.Paths()))
	}
}

func TestExpandPathsDoesNotHideSourceBehindNodeModules(t *testing.T) {
	prev := maxExpandCandidates
	maxExpandCandidates = 8
	t.Cleanup(func() { maxExpandCandidates = prev })

	root := t.TempDir()
	nm := filepath.Join(root, "node_modules", "pkg")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(nm, fmt.Sprintf("m%d.js", i)), []byte("module.exports=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: root, Paths: []string{"."}, Budget: "large",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(sc.Paths(), " "), "src/main.go") {
		t.Fatalf("source hidden behind node_modules: paths=%v truncated=%v", sc.Paths(), sc.Truncated)
	}
	if sc.Truncated {
		t.Fatal("default excludes should SkipDir node_modules; one source file must not truncate")
	}
}

func TestExpandPathsExactCapIsNotTruncated(t *testing.T) {
	prev := maxExpandCandidates
	maxExpandCandidates = 4
	t.Cleanup(func() { maxExpandCandidates = prev })

	root := t.TempDir()
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.go", i)), []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: root, Paths: []string{"."}, Budget: "large",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Truncated {
		t.Fatal("exactly maxExpandCandidates files must not set Truncated")
	}
	if len(sc.Paths()) != 4 {
		t.Fatalf("paths=%d want 4", len(sc.Paths()))
	}
}

func TestExpandPathsIncludeAllStillCaps(t *testing.T) {
	prev := maxExpandCandidates
	maxExpandCandidates = 8
	t.Cleanup(func() { maxExpandCandidates = prev })

	root := t.TempDir()
	nm := filepath.Join(root, "node_modules", "pkg")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(nm, fmt.Sprintf("m%d.js", i)), []byte("module.exports=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: root, Paths: []string{"."}, Budget: "large", IncludeAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Truncated {
		t.Fatal("IncludeAll must still honor the walk cap inside node_modules")
	}
	if len(sc.Paths()) > 8 {
		t.Fatalf("paths=%d want <=8", len(sc.Paths()))
	}
}

func TestRecordExcludedCapsList(t *testing.T) {
	sc := &Scope{}
	for i := 0; i < maxExcludedFiles+10; i++ {
		recordExcluded(sc, fmt.Sprintf("f%d.go", i), "over budget (max files)")
	}
	if len(sc.Excluded) != maxExcludedFiles {
		t.Fatalf("excluded=%d want %d", len(sc.Excluded), maxExcludedFiles)
	}
	last := sc.Excluded[len(sc.Excluded)-1]
	if last.Path != "…" || !strings.Contains(last.Reason, "capped") {
		t.Fatalf("last entry = %+v", last)
	}
}

func TestResolveScopeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := &fakeGit{repo: true, changed: []string{"a.go", "b.go", "c.go"}}
	files := memFS(map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
		"c.go": "package c\n",
	})
	_, err := resolveScope(ctx, ScopeRequest{Workspace: "/ws", Diff: "main...HEAD"}, g.run, files)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRespectsContextCancellation(t *testing.T) {
	sc := testScope(t)
	block := make(chan struct{})
	rev := &blockingReviewer{release: block}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, rev, sc, Request{Profile: GeneralProfile()})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !(errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "cancel")) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		close(block)
		t.Fatal("Run did not stop after cancellation")
	}
}

type blockingReviewer struct {
	release chan struct{}
}

func (r *blockingReviewer) Ask(ctx context.Context, _, _ string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.release:
		return `{"findings":[]}`, nil
	}
}
func (r *blockingReviewer) Model() string { return "blocking" }

func TestEnsembleMemberFailureFailsRun(t *testing.T) {
	ok := &ensembleFake{model: "ok", replies: []string{`{"findings":[]}`}}
	bad := &errReviewer{msg: "model unavailable"}
	ens, err := NewEnsembleReviewer([]EnsembleMember{
		{Name: "alpha", Reviewer: ok},
		{Name: "beta", Reviewer: bad},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), ens, testScope(t), Request{Profile: GeneralProfile()})
	if err == nil || !strings.Contains(err.Error(), "ensemble member") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsembleMemberFailureCancelsPeers(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	slow := &peerWaitReviewer{started: started, release: release}
	bad := &errReviewer{msg: "boom"}
	ens, err := NewEnsembleReviewer([]EnsembleMember{
		{Name: "slow", Reviewer: slow},
		{Name: "bad", Reviewer: bad},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ens.Ask(context.Background(), "", ""); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensemble did not fail promptly")
	}
	select {
	case <-slow.cancelled:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("slow peer was not cancelled")
	}
}

type errReviewer struct{ msg string }

func (e *errReviewer) Ask(ctx context.Context, system, prompt string) (string, error) {
	_ = ctx
	_ = system
	_ = prompt
	return "", errors.New(e.msg)
}
func (e *errReviewer) Model() string { return "err" }

type peerWaitReviewer struct {
	started   chan<- struct{}
	release   <-chan struct{}
	cancelled chan struct{}
}

func (r *peerWaitReviewer) Ask(ctx context.Context, _, _ string) (string, error) {
	if r.cancelled == nil {
		r.cancelled = make(chan struct{})
	}
	select {
	case r.started <- struct{}{}:
	case <-ctx.Done():
		close(r.cancelled)
		return "", ctx.Err()
	}
	select {
	case <-ctx.Done():
		close(r.cancelled)
		return "", ctx.Err()
	case <-r.release:
		return `{"findings":[]}`, nil
	}
}
func (r *peerWaitReviewer) Model() string { return "slow" }
