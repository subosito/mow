package review

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/subosito/mow"
)

// Reviewer is the minimal engine surface the workflow needs. mow.Engine
// satisfies it; tests supply a fake so the two-pass logic is testable without
// a model. Kept narrow on purpose — the workflow must not reach for write or
// shell capabilities.
type Reviewer interface {
	// Ask runs one read-only turn with the given system text and returns the
	// assistant reply.
	Ask(ctx context.Context, system, prompt string) (string, error)
	// Model reports the model id for report provenance.
	Model() string
}

// Request is a complete review invocation.
type Request struct {
	Scope   ScopeRequest
	Profile *Profile
	// MinSeverity filters the reported findings (zero → profile default).
	MinSeverity Severity
	// IncludeUnverified keeps candidates the verification pass could not
	// confirm. Off by default: unverified findings are the main noise source.
	IncludeUnverified bool
	// SkipVerification runs the candidate pass only. Faster and cheaper, but
	// the report is explicitly marked as unverified.
	SkipVerification bool
	// Verifier runs pass two when set. When nil, pass two uses the primary
	// reviewer (or the first ensemble member).
	Verifier Reviewer
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// Result is a finished review: the report plus the resolved scope.
type Result struct {
	Report *Report
	Scope  *Scope
}

// Run executes the two-pass review: candidate discovery, then verification,
// then validation against the resolved scope.
//
// The two passes are separate model calls on purpose. A single prompt that asks
// for findings "and then double-check them" lets the model rationalize its own
// output; a second call that only rules on ids has to re-derive the evidence.
func Run(ctx context.Context, rev Reviewer, sc *Scope, req Request) (*Result, error) {
	if rev == nil {
		return nil, fmt.Errorf("review: nil reviewer")
	}
	prof := req.Profile
	if prof == nil {
		prof = GeneralProfile()
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}
	if sc == nil {
		return nil, fmt.Errorf("review: nil scope")
	}
	started := now()

	rep := NewReport(prof.Name)
	rep.Run = RunInfo{
		Tool:             prof.Command,
		Version:          strings.TrimPrefix(mow.VersionString(), "mow "),
		Model:            rev.Model(),
		Commit:           sc.Git.Commit,
		Branch:           sc.Git.Branch,
		StartedAt:        started,
		Truncated:        sc.Truncated,
		TruncationReason: sc.TruncReason,
	}
	// Record pass-two provenance when verification will run. An explicit
	// Request.Verifier is always named (even if the model id matches pass one);
	// otherwise only record when the resolved verifier differs from the
	// candidate-reviewer Model() string (e.g. ensemble default).
	if !req.SkipVerification {
		if req.Verifier != nil {
			rep.Run.VerifierModel = req.Verifier.Model()
		} else if v := resolveVerifier(req, rev); v != nil && v.Model() != rev.Model() {
			rep.Run.VerifierModel = v.Model()
		}
	}
	rep.Scope = sc.Info(req.Scope)

	// Nothing in scope is a successful, empty review — not an error and not a
	// clean bill of health.
	if sc.Empty() {
		rep.Summary = "No files in scope; nothing was reviewed."
		rep.Notes = append(rep.Notes, "Review scope was empty — check the selector, excludes, or budget.")
		finish(rep, started, now)
		return &Result{Report: rep, Scope: sc}, nil
	}

	valOpt := ValidationOptions{
		Profile:   prof,
		InScope:   sc.InScope,
		FileLines: sc.FileLines,
	}

	// --- Pass 1: candidate discovery ---
	reply, err := rev.Ask(ctx, systemPrompt(prof), candidatePrompt(prof, sc))
	if err != nil {
		return nil, fmt.Errorf("review: candidate pass: %w", err)
	}
	cand, err := parseCandidates(reply)
	if err != nil {
		return nil, err
	}
	candidates, issues := Validate(cand.Findings, sc.Workspace, valOpt)
	// Pass 1 is never authoritative about verification provenance. The model
	// may include a `verified` field despite the contract; only verifyPass may
	// set it true.
	for i := range candidates {
		candidates[i].Verified = false
		candidates[i].VerificationNotes = ""
	}
	dropped := len(issues)
	for _, is := range issues {
		rep.Notes = append(rep.Notes, "pass 1: "+is.String())
	}

	// --- Pass 2: verification ---
	switch {
	case len(candidates) == 0:
		// Nothing survived pass 1; a verification call would have no subject.
	case req.SkipVerification:
		rep.Notes = append(rep.Notes, "Verification pass skipped: findings are unverified.")
	default:
		verifier := resolveVerifier(req, rev)
		verified, vnotes, verr := verifyPass(ctx, verifier, prof, sc, candidates, req)
		if verr != nil {
			return nil, verr
		}
		candidates = verified
		rep.Notes = append(rep.Notes, vnotes...)
	}

	// Unverified findings are suppressed unless asked for, since the whole
	// point of pass 2 is to keep false positives out of the report.
	kept := make([]Finding, 0, len(candidates))
	for _, f := range candidates {
		switch {
		case !f.Verified && !req.SkipVerification && !req.IncludeUnverified:
			dropped++
		case f.Severity < effectiveMinSeverity(req, prof):
			dropped++
		default:
			kept = append(kept, f)
		}
	}
	// Re-number after filtering so ids stay contiguous in the output.
	renumber(kept, prof.Name)

	applySecurityEvidenceLevel(prof.Name, kept)
	rep.Findings = kept
	rep.Suppressed = dropped
	rep.Recount()
	rep.Summary = buildSummary(rep, prof, cand.Summary)
	for _, n := range cand.Notes {
		if n = strings.TrimSpace(n); n != "" {
			rep.Notes = append(rep.Notes, "pass 1 note: "+n)
		}
	}
	finish(rep, started, now)
	return &Result{Report: RedactReport(rep), Scope: sc}, nil
}

// verifyPass runs the second model call and applies its verdicts.
func verifyPass(ctx context.Context, rev Reviewer, prof *Profile, sc *Scope, cands []Finding, req Request) ([]Finding, []string, error) {
	reply, err := rev.Ask(ctx, systemPrompt(prof), verifyPrompt(prof, sc, cands))
	if err != nil {
		return nil, nil, fmt.Errorf("review: verification pass: %w", err)
	}
	env, err := parseVerdicts(reply)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]Verdict{}
	want := make(map[string]bool, len(cands))
	for _, f := range cands {
		want[f.ID] = true
	}
	for _, v := range env.Verdicts {
		id := strings.TrimSpace(v.ID)
		if id == "" {
			return nil, nil, fmt.Errorf("review: verification pass returned a verdict with an empty id")
		}
		if !want[id] {
			return nil, nil, fmt.Errorf("review: verification pass returned unknown candidate id %q", id)
		}
		if _, dup := byID[id]; dup {
			return nil, nil, fmt.Errorf("review: verification pass returned duplicate verdict for %q", id)
		}
		byID[id] = v
	}
	var notes []string
	out := make([]Finding, 0, len(cands))
	for _, f := range cands {
		v, ok := byID[f.ID]
		if !ok {
			// No verdict at all: treat as unconfirmed rather than assuming pass.
			f.Verified = false
			f.VerificationNotes = appendNote(f.VerificationNotes, "verification pass returned no verdict for this candidate")
			out = append(out, f)
			continue
		}
		if v.Rejected() {
			notes = append(notes, fmt.Sprintf("pass 2 rejected %q: %s", f.Title, reasonOr(v.Reason, "no reason given")))
			continue
		}
		f.Verified = v.Confirmed()
		if r := strings.TrimSpace(v.Reason); r != "" {
			f.VerificationNotes = appendNote(f.VerificationNotes, r)
		}
		// The verifier may correct severity/confidence and, on mow sec, structured
		// evidence fields returned explicitly in evidence_fields.
		if s, ok := ParseSeverity(v.Severity); ok && s != f.Severity {
			notes = append(notes, fmt.Sprintf("pass 2 adjusted severity of %q: %s → %s", f.Title, f.Severity, s))
			f.Severity = s
		}
		if c, ok := ParseConfidence(v.Confidence); ok {
			f.Confidence = c
		}
		if corrNotes, err := applyVerdictEvidenceCorrections(&f, v, prof); err != nil {
			return nil, nil, err
		} else {
			notes = append(notes, corrNotes...)
		}
		if !f.Verified {
			f.VerificationNotes = appendNote(f.VerificationNotes, "not confirmed by the verification pass")
		}
		f.VerificationNotes = clampText(redactSecrets(f.VerificationNotes))
		out = append(out, f)
	}
	sortFindings(out)
	return out, notes, nil
}

// resolveVerifier picks the pass-two reviewer. An explicit Request.Verifier
// overrides the ensemble default (first member) or the single-engine default.
func resolveVerifier(req Request, rev Reviewer) Reviewer {
	if req.Verifier != nil {
		return req.Verifier
	}
	if ensemble, ok := rev.(*EnsembleReviewer); ok {
		return ensemble.verifier()
	}
	return rev
}

// effectiveMinSeverity resolves the report floor.
func effectiveMinSeverity(req Request, prof *Profile) Severity {
	if req.MinSeverity.Valid() {
		return req.MinSeverity
	}
	return prof.MinSeverity
}

// renumber reassigns contiguous ids after filtering.
func renumber(fs []Finding, profile string) {
	prefix := findingIDPrefix(profile)
	for i := range fs {
		fs[i].ID = fmt.Sprintf("%s-%03d", prefix, i+1)
	}
}

// buildSummary prefers a deterministic count sentence over model prose, then
// appends the model's summary when it adds something.
func buildSummary(rep *Report, prof *Profile, modelSummary string) string {
	var b strings.Builder
	if rep.Counts.Total == 0 {
		b.WriteString("No findings at or above the reporting threshold.")
		if rep.Suppressed > 0 {
			fmt.Fprintf(&b, " %d candidate(s) were suppressed or rejected.", rep.Suppressed)
		}
	} else {
		fmt.Fprintf(&b, "%d finding(s): %s.", rep.Counts.Total, severityBreakdown(rep.Counts))
		if rep.Suppressed > 0 {
			fmt.Fprintf(&b, " %d candidate(s) suppressed or rejected.", rep.Suppressed)
		}
	}
	if rep.Run.Truncated {
		b.WriteString(" Scope was truncated, so coverage is partial.")
	}
	if s := strings.TrimSpace(modelSummary); s != "" {
		b.WriteString(" ")
		b.WriteString(s)
	}
	_ = prof
	return b.String()
}

// severityBreakdown renders "1 high, 2 medium" for non-zero buckets.
func severityBreakdown(c Counts) string {
	var parts []string
	for _, p := range []struct {
		n    int
		name string
	}{
		{c.Critical, "critical"}, {c.High, "high"}, {c.Medium, "medium"},
		{c.Low, "low"}, {c.Info, "info"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func reasonOr(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

// finish stamps the run duration.
func finish(rep *Report, started time.Time, now func() time.Time) {
	rep.Run.DurationMS = now().Sub(started).Milliseconds()
}
