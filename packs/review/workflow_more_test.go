package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRunFailsLoudlyOnBadJSON(t *testing.T) {
	sc := testScope(t)
	// A reply with no JSON must be a tooling error, never "clean review".
	rev := &fakeReviewer{replies: []string{"The code looks fine to me!"}}
	if _, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile()}); !errors.Is(err, ErrNoJSON) {
		t.Fatalf("err = %v want ErrNoJSON", err)
	}
	// A candidate envelope must explicitly contain a non-null findings array.
	for _, reply := range []string{`{"summary":"looks fine"}`, `{"findings":null}`} {
		rev := &fakeReviewer{replies: []string{reply}}
		if _, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile()}); err == nil || !strings.Contains(err.Error(), "findings") {
			t.Fatalf("reply=%s err=%v want findings contract error", reply, err)
		}
	}
	// Same for a contract violation in pass 2.
	rev2 := &fakeReviewer{replies: []string{candidateReply, "looks good, ship it"}}
	if _, err := Run(context.Background(), rev2, sc, Request{Profile: GeneralProfile()}); err == nil {
		t.Fatal("want error when the verification pass returns no JSON")
	}
	for _, reply := range []string{`{"summary":"confirmed"}`, `{"verdicts":null}`} {
		rev := &fakeReviewer{replies: []string{candidateReply, reply}}
		if _, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile()}); err == nil || !strings.Contains(err.Error(), "verdicts") {
			t.Fatalf("reply=%s err=%v want verdicts contract error", reply, err)
		}
	}
}

func TestRunPropagatesModelError(t *testing.T) {
	sc := testScope(t)
	rev := &fakeReviewer{err: errors.New("gateway down")}
	_, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile()})
	if err == nil || !strings.Contains(err.Error(), "gateway down") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "candidate pass") {
		t.Errorf("error should say which pass failed: %v", err)
	}
}

func TestRunEmptyScopeIsSuccessNotCleanBill(t *testing.T) {
	g := &fakeGit{repo: true, changed: nil}
	sc, err := resolveScope(context.Background(), ScopeRequest{Workspace: "/ws", Diff: "main...HEAD"}, g.run, memFS(nil))
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	rev := &fakeReviewer{} // must not be called
	res, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev.calls != 0 {
		t.Errorf("empty scope should not call the model, got %d calls", rev.calls)
	}
	if len(res.Report.Findings) != 0 {
		t.Error("expected no findings")
	}
	if !strings.Contains(res.Report.Summary, "nothing was reviewed") {
		t.Errorf("summary must not imply a clean review: %q", res.Report.Summary)
	}
}

func TestRunSkipVerification(t *testing.T) {
	sc := testScope(t)
	forged := strings.Replace(candidateReply, `"path":"internal/db/query.go"`, `"verified":true,"path":"internal/db/query.go"`, 1)
	rev := &fakeReviewer{replies: []string{forged}}
	res, err := Run(context.Background(), rev, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), SkipVerification: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev.calls != 1 {
		t.Fatalf("skip should make exactly one call, got %d", rev.calls)
	}
	if len(res.Report.Findings) != 2 {
		t.Fatalf("findings = %d want 2", len(res.Report.Findings))
	}
	for _, f := range res.Report.Findings {
		if f.Verified {
			t.Fatalf("skip verification must force verified=false: %+v", f)
		}
	}
	if !hasNote(res.Report.Notes, "Verification pass skipped") {
		t.Errorf("report must disclose that verification was skipped: %v", res.Report.Notes)
	}
}

func TestRunMinSeverityFilter(t *testing.T) {
	sc := testScope(t)
	verify := `{"verdicts":[
	  {"id":"review-001","status":"confirmed"},
	  {"id":"review-002","status":"confirmed"}
	]}`
	rev := &fakeReviewer{replies: []string{candidateReply, verify}}
	res, err := Run(context.Background(), rev, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), MinSeverity: SevHigh,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Report.Findings) != 1 || res.Report.Findings[0].Severity != SevHigh {
		t.Fatalf("min-severity filter failed: %+v", res.Report.Findings)
	}
	if res.Report.Suppressed != 1 {
		t.Errorf("suppressed = %d want 1", res.Report.Suppressed)
	}
}

func TestRunSecurityProfileVerdictAdjustsSeverity(t *testing.T) {
	sc := testScope(t)
	cand := `{"findings":[
	  {"title":"Missing authorization check","category":"authorization","severity":"critical","confidence":"low",
	   "path":"internal/api/users.go","start_line":10,
	   "evidence":"no ownership check before update","impact":"cross-tenant write","recommendation":"check owner",
	   "attack_surface":"HTTP path parameter"}
	]}`
	verify := `{"verdicts":[{"id":"sec-001","status":"confirmed","severity":"high","confidence":"medium","reason":"needs auth session"}]}`
	rev := &fakeReviewer{replies: []string{cand, verify}}
	res, err := Run(context.Background(), rev, sc, Request{Profile: SecurityProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Report.Findings) != 1 {
		t.Fatalf("findings = %+v", res.Report.Findings)
	}
	f := res.Report.Findings[0]
	if f.Severity != SevHigh {
		t.Errorf("verifier severity correction not applied: %v", f.Severity)
	}
	if f.Confidence != ConfMedium {
		t.Errorf("verifier confidence correction not applied: %v", f.Confidence)
	}
	if f.Category != CatAuthz {
		t.Errorf("category = %q", f.Category)
	}
	if f.Extra["attack_surface"] != "HTTP path parameter" {
		t.Errorf("security extra lost: %+v", f.Extra)
	}
	if res.Report.Profile != "security" || res.Report.Run.Tool != "mow sec" {
		t.Errorf("profile provenance = %q / %q", res.Report.Profile, res.Report.Run.Tool)
	}
	if !hasNote(res.Report.Notes, "adjusted severity") {
		t.Errorf("severity change should be disclosed: %v", res.Report.Notes)
	}
}

func TestRunDropsOutOfScopeCandidateWithNote(t *testing.T) {
	sc := testScope(t)
	cand := `{"findings":[
	  {"title":"Bug in unrelated file","category":"correctness","severity":"high","confidence":"high",
	   "path":"cmd/other/main.go","start_line":3,"evidence":"not in scope"},
	  {"title":"Hallucinated line","category":"correctness","severity":"high","confidence":"high",
	   "path":"internal/db/query.go","start_line":9999,"evidence":"beyond EOF"}
	]}`
	verify := `{"verdicts":[{"id":"review-001","status":"confirmed"}]}`
	rev := &fakeReviewer{replies: []string{cand, verify}}
	res, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The out-of-scope path is dropped in validation; the bad line is clamped.
	// (An out-of-scope path that also doesn't exist is caught by the existence
	// check first — either way it must be dropped and explained.)
	if !hasNote(res.Report.Notes, "cmd/other/main.go") {
		t.Errorf("notes should record the out-of-scope drop: %v", res.Report.Notes)
	}
	for _, f := range res.Report.Findings {
		if f.Path == "cmd/other/main.go" {
			t.Error("out-of-scope finding leaked into the report")
		}
		if f.StartLine > 0 {
			if n, _ := sc.FileLines(f.Path); f.StartLine > n {
				t.Errorf("line %d exceeds file length %d", f.StartLine, n)
			}
		}
	}
}

func TestPromptsCarryContractAndScope(t *testing.T) {
	sc := testScope(t)
	verify := `{"verdicts":[{"id":"sec-001","status":"confirmed"},{"id":"sec-002","status":"confirmed"}]}`
	rev := &fakeReviewer{replies: []string{candidateReply, verify}}
	if _, err := Run(context.Background(), rev, sc, Request{Profile: SecurityProfile(), Now: fixedNow()}); err != nil {
		t.Fatalf("run: %v", err)
	}
	p1, p2 := rev.prompts[0], rev.prompts[1]
	for _, want := range []string{"Pass 1 of 2", "internal/api/users.go", `"findings"`, "workspace-relative"} {
		if !strings.Contains(p1, want) {
			t.Errorf("candidate prompt missing %q", want)
		}
	}
	// Line-numbered content is what lets the model cite real lines.
	if !strings.Contains(p1, "1| // line") {
		t.Error("candidate prompt should include line-numbered content")
	}
	for _, want := range []string{
		"Pass 2 of 2", `"verdicts"`, "sec-001", "sec-002",
		"attacker-controlled", "source→transform→sink", "model-verified", "evidence_fields",
	} {
		if !strings.Contains(p2, want) {
			t.Errorf("verify prompt missing %q", want)
		}
	}
	for _, want := range []string{"source", "sink", "sanitizers_considered", "source → transform → sink"} {
		if !strings.Contains(p1, want) {
			t.Errorf("security candidate prompt missing evidence cue %q", want)
		}
	}
	// The verifier must not be handed the raw candidate JSON envelope.
	if strings.Contains(p2, `"findings"`) {
		t.Error("verify prompt should present digests, not the candidate JSON contract")
	}
	if !strings.Contains(p2, "Candidate source excerpts") || !strings.Contains(p2, "10| // line") {
		t.Errorf("verify prompt should include bounded line-numbered evidence: %s", p2)
	}
	// Security system prompt should set the adversarial persona for both passes.
	for i, sys := range rev.systems {
		if !strings.Contains(sys, "security reviewer") {
			t.Errorf("system prompt %d lost the security persona", i)
		}
		if !strings.Contains(sys, "advisory") {
			t.Errorf("system prompt %d lost the advisory rule", i)
		}
	}
}

// A pass that exhausts its turn budget must fail loudly. Turn exhaustion
// arrives from the engine as an error, so an adapter that only inspected
// StopReason would let it fall through as an empty reply — and an empty reply
// is indistinguishable from "no findings", i.e. a silent false clean review.
func TestReviewerReportsTurnExhaustion(t *testing.T) {
	rev := &fakeReviewer{err: fmt.Errorf("agent: max turns exceeded: 12")}
	sc := testScope(t)
	_, err := Run(context.Background(), rev, sc, Request{Profile: SecurityProfile()})
	if err == nil {
		t.Fatal("turn exhaustion must not produce a report")
	}
	if !strings.Contains(err.Error(), "max turns") {
		t.Errorf("error should name the cause: %v", err)
	}
}

// Budgets must leave room to answer after exploring: a cap so tight that the
// pass cannot emit its JSON turns every review into a failed run.
func TestBudgetTurnsAllowExplorationAndAnswer(t *testing.T) {
	for _, name := range BudgetNames() {
		b, ok := LookupBudget(name)
		if !ok {
			t.Fatalf("budget %q missing", name)
		}
		// Observed floor: a single-file security review used ~10 tool calls
		// before answering; anything under 20 risks starving the report.
		if b.MaxTurns < 20 {
			t.Errorf("budget %q MaxTurns=%d is too tight to explore and still answer", name, b.MaxTurns)
		}
	}
}

func TestVerificationRejectsDuplicateAndUnknownIDs(t *testing.T) {
	sc := testScope(t)
	cases := []struct {
		name, reply, want string
	}{
		{"duplicate", `{"verdicts":[{"id":"review-001","status":"confirmed"},{"id":"review-001","status":"rejected"}]}`, "duplicate"},
		{"unknown", `{"verdicts":[{"id":"review-999","status":"confirmed"}]}`, "unknown"},
		{"empty", `{"verdicts":[{"id":"","status":"confirmed"}]}`, "empty id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rev := &fakeReviewer{replies: []string{candidateReply, tc.reply}}
			if _, err := Run(context.Background(), rev, sc, Request{Profile: GeneralProfile()}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestRunNilScope(t *testing.T) {
	if _, err := Run(context.Background(), &fakeReviewer{}, nil, Request{}); err == nil {
		t.Fatal("want error for nil scope")
	}
}

func TestRunNilReviewer(t *testing.T) {
	if _, err := Run(context.Background(), nil, testScope(t), Request{}); err == nil || !strings.Contains(err.Error(), "nil reviewer") {
		t.Fatalf("err=%v want nil reviewer error", err)
	}
}

func TestRunSecurityVerdictCorrectsEvidenceFields(t *testing.T) {
	sc := testScope(t)
	cand := `{"findings":[{
	  "title":"Path traversal","category":"path-traversal","severity":"high","confidence":"medium",
	  "path":"internal/api/users.go","start_line":10,
	  "evidence":"user path joined into open","impact":"read arbitrary file","recommendation":"validate path",
	  "source":"HTTP path","sink":"os.Open","reachability":"public route"
	}]}`
	verify := `{"verdicts":[{"id":"sec-001","status":"confirmed","evidence_fields":{
	  "reachability":"conditional — auth required",
	  "sink":null
	}}]}`
	rev := &fakeReviewer{replies: []string{cand, verify}}
	res, err := Run(context.Background(), rev, sc, Request{Profile: SecurityProfile(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	f := res.Report.Findings[0]
	if f.Extra["reachability"] != "conditional — auth required" {
		t.Fatalf("reachability = %q", f.Extra["reachability"])
	}
	if _, ok := f.Extra["sink"]; ok {
		t.Fatal("sink should be cleared by verifier")
	}
	if f.Extra["evidence_level"] != "code-supported" {
		t.Fatalf("evidence_level = %q want code-supported after sink cleared", f.Extra["evidence_level"])
	}
}

func TestRunIncludeUnverifiedNeverGetsModelVerifiedLevel(t *testing.T) {
	sc := testScope(t)
	cand := `{"findings":[{
	  "title":"Path traversal","category":"path-traversal","severity":"high","confidence":"high",
	  "path":"internal/api/users.go","start_line":10,
	  "evidence":"user path joined into open","impact":"read arbitrary file","recommendation":"validate path",
	  "source":"HTTP path","sink":"os.Open","reachability":"public route"
	}]}`
	verify := `{"verdicts":[{"id":"sec-001","status":"uncertain","reason":"cannot confirm auth"}]}`
	rev := &fakeReviewer{replies: []string{cand, verify}}
	res, err := Run(context.Background(), rev, sc, Request{
		Profile: SecurityProfile(), Now: fixedNow(), IncludeUnverified: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Report.Findings) != 1 {
		t.Fatalf("findings = %d", len(res.Report.Findings))
	}
	if got := res.Report.Findings[0].Extra["evidence_level"]; got == "model-verified" {
		t.Fatalf("unverified finding must not be model-verified, got %q", got)
	}
}

func TestRunUsesDedicatedVerifier(t *testing.T) {
	sc := testScope(t)
	candidate := &fakeReviewer{
		model:   "candidate-model",
		replies: []string{candidateReply},
	}
	verifier := &fakeReviewer{
		model:   "verifier-model",
		replies: []string{`{"verdicts":[{"id":"review-001","status":"confirmed"},{"id":"review-002","status":"rejected","reason":"fine"}]}`},
	}
	res, err := Run(context.Background(), candidate, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), Verifier: verifier,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if candidate.calls != 1 || verifier.calls != 1 {
		t.Fatalf("candidate calls=%d verifier calls=%d", candidate.calls, verifier.calls)
	}
	if res.Report.Run.VerifierModel != "verifier-model" {
		t.Fatalf("VerifierModel = %q", res.Report.Run.VerifierModel)
	}
}

func TestRunRecordsExplicitVerifierEvenWhenModelMatches(t *testing.T) {
	sc := testScope(t)
	candidate := &fakeReviewer{
		model:   "same-model",
		replies: []string{candidateReply},
	}
	verifier := &fakeReviewer{
		model:   "same-model",
		replies: []string{`{"verdicts":[{"id":"review-001","status":"confirmed"},{"id":"review-002","status":"rejected","reason":"fine"}]}`},
	}
	res, err := Run(context.Background(), candidate, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), Verifier: verifier,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Report.Run.VerifierModel != "same-model" {
		t.Fatalf("VerifierModel = %q want same-model for explicit verifier", res.Report.Run.VerifierModel)
	}
}

func TestRunSkipVerificationOmitsVerifierModel(t *testing.T) {
	sc := testScope(t)
	candidate := &fakeReviewer{model: "candidate-model", replies: []string{candidateReply}}
	verifier := &fakeReviewer{model: "verifier-model"}
	res, err := Run(context.Background(), candidate, sc, Request{
		Profile: GeneralProfile(), Now: fixedNow(), Verifier: verifier, SkipVerification: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if verifier.calls != 0 {
		t.Fatalf("verifier should not run when skipped, calls=%d", verifier.calls)
	}
	if res.Report.Run.VerifierModel != "" {
		t.Fatalf("VerifierModel = %q want empty when verification skipped", res.Report.Run.VerifierModel)
	}
}
