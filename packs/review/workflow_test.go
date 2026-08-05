package review

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeReviewer replays scripted replies, one per Ask call.
type fakeReviewer struct {
	replies []string
	systems []string
	prompts []string
	err     error
	calls   int
}

func (f *fakeReviewer) Ask(ctx context.Context, system, prompt string) (string, error) {
	f.calls++
	f.systems = append(f.systems, system)
	f.prompts = append(f.prompts, prompt)
	if f.err != nil {
		return "", f.err
	}
	if f.calls > len(f.replies) {
		return "", fmt.Errorf("fakeReviewer: unexpected call %d", f.calls)
	}
	return f.replies[f.calls-1], nil
}

func (f *fakeReviewer) Model() string { return "test-model" }

// testScope builds an in-memory scope over two files.
func testScope(t *testing.T) *Scope {
	t.Helper()
	g := &fakeGit{repo: true, changed: []string{"internal/api/users.go", "internal/db/query.go"}}
	files := map[string]string{
		"internal/api/users.go": strings.Repeat("// line\n", 120),
		"internal/db/query.go":  strings.Repeat("// line\n", 60),
	}
	sc, err := resolveScope(context.Background(), ScopeRequest{Workspace: "/ws", Diff: "main...HEAD"}, g.run, memFS(files))
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if len(sc.Files) != 2 {
		t.Fatalf("scope files = %v", sc.Paths())
	}
	return sc
}

func fixedNow() func() time.Time {
	n := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		n = n.Add(250 * time.Millisecond)
		return n
	}
}

const candidateReply = `{"findings":[
  {"title":"Possible nil dereference","category":"correctness","severity":"high","confidence":"medium",
   "path":"internal/api/users.go","start_line":10,"end_line":12,
   "evidence":"findUser can return nil,nil","impact":"panic","recommendation":"nil check"},
  {"title":"Unchecked error from Close","category":"error-handling","severity":"medium","confidence":"high",
   "path":"internal/db/query.go","start_line":5,
   "evidence":"rows.Close error ignored","impact":"leak","recommendation":"check err"}
],"summary":"two issues"}`

func TestRunTwoPassConfirmsAndRejects(t *testing.T) {
	sc := testScope(t)
	verify := `{"verdicts":[
	  {"id":"review-001","status":"confirmed","reason":"caller does not check"},
	  {"id":"review-002","status":"rejected","reason":"deferred close is fine here"}
	],"summary":"one survived"}`
	rev := &fakeReviewer{replies: []string{candidateReply, verify}}

	res, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev.calls != 2 {
		t.Fatalf("want two model calls, got %d", rev.calls)
	}
	rep := res.Report
	if len(rep.Findings) != 1 {
		t.Fatalf("findings = %+v", rep.Findings)
	}
	f := rep.Findings[0]
	if f.Title != "Possible nil dereference" {
		t.Errorf("wrong finding survived: %q", f.Title)
	}
	if !f.Verified {
		t.Error("confirmed finding should be marked verified")
	}
	if !strings.Contains(f.VerificationNotes, "caller does not check") {
		t.Errorf("verification reason lost: %q", f.VerificationNotes)
	}
	if f.ID != "review-001" {
		t.Errorf("ids should be contiguous after filtering: %q", f.ID)
	}
	if rep.Suppressed != 0 {
		t.Errorf("rejected findings are not 'suppressed', got %d", rep.Suppressed)
	}
	if rep.Counts.High != 1 || rep.Counts.Total != 1 {
		t.Errorf("counts = %+v", rep.Counts)
	}
	if rep.Run.Model != "test-model" || rep.Run.Tool != "mow review" {
		t.Errorf("run info = %+v", rep.Run)
	}
	if rep.Run.DurationMS <= 0 {
		t.Error("duration should be recorded")
	}
	if !rep.Advisory || rep.SchemaVersion != SchemaVersion {
		t.Error("envelope invariants lost")
	}
	// The rejection should be explained in notes.
	if !hasNote(rep.Notes, "rejected") {
		t.Errorf("notes should record the rejection: %v", rep.Notes)
	}
}

func TestRunSuppressesUnverified(t *testing.T) {
	sc := testScope(t)
	// Verifier is uncertain about both candidates.
	verify := `{"verdicts":[
	  {"id":"review-001","status":"uncertain","reason":"cannot tell"},
	  {"id":"review-002","status":"uncertain"}
	]}`
	rev := &fakeReviewer{replies: []string{candidateReply, verify}}
	res, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Report.Findings) != 0 {
		t.Fatalf("unverified findings must be suppressed by default: %+v", res.Report.Findings)
	}
	if res.Report.Suppressed != 2 {
		t.Errorf("suppressed = %d want 2", res.Report.Suppressed)
	}
	if !strings.Contains(res.Report.Summary, "suppressed") {
		t.Errorf("summary should mention suppression: %q", res.Report.Summary)
	}

	// With IncludeUnverified they come back, still flagged.
	rev2 := &fakeReviewer{replies: []string{candidateReply, verify}}
	res2, err := Run(context.Background(), rev2, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), IncludeUnverified: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res2.Report.Findings) != 2 {
		t.Fatalf("findings = %d want 2", len(res2.Report.Findings))
	}
	for _, f := range res2.Report.Findings {
		if f.Verified {
			t.Errorf("uncertain finding marked verified: %q", f.Title)
		}
		if !strings.Contains(f.VerificationNotes, "not confirmed") {
			t.Errorf("missing unconfirmed note: %q", f.VerificationNotes)
		}
	}
}

func TestRunMissingVerdictIsNotAPass(t *testing.T) {
	sc := testScope(t)
	// Verifier only rules on the first candidate.
	verify := `{"verdicts":[{"id":"review-001","status":"confirmed"}]}`
	rev := &fakeReviewer{replies: []string{candidateReply, verify}}
	res, err := Run(context.Background(), rev, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), IncludeUnverified: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var silent *Finding
	for i := range res.Report.Findings {
		if strings.Contains(res.Report.Findings[i].Title, "Unchecked error") {
			silent = &res.Report.Findings[i]
		}
	}
	if silent == nil {
		t.Fatalf("candidate missing: %+v", res.Report.Findings)
	}
	if silent.Verified {
		t.Error("a candidate with no verdict must not count as verified")
	}
	if !strings.Contains(silent.VerificationNotes, "no verdict") {
		t.Errorf("note = %q", silent.VerificationNotes)
	}
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
