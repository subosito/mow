// Package review is the shared foundation for AI-assisted code review in mow:
// a stable report schema, scope resolution, a two-pass (candidate → verify)
// workflow over the engine, and renderers for text/JSON/SARIF.
//
// Two profiles ship with the pack and drive the CLI:
//
//	general  -> mow review   (correctness, tests, maintainability, API compat)
//	security -> mow sec      (adversarial: authz, injection, secrets, crypto)
//
// Findings are always advisory: they are model-produced, evidence-backed
// suggestions, never a proof of correctness or security.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// SchemaVersion is the version of the report envelope emitted by this package.
// Bump on breaking changes to the JSON shape; consumers should check it.
const SchemaVersion = 1

// Location is a source reference. Lines are 1-based and inclusive; EndLine 0
// means "single line" (or unknown) and renders as just StartLine.
type Location struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Role explains why this location is attached: "primary", "evidence",
	// "sink", "source", "caller", "test". Free-form, defaults to "evidence".
	Role string `json:"role,omitempty"`
	// Snippet is an optional short excerpt captured by validation for renderers.
	Snippet string `json:"snippet,omitempty"`
}

// Finding is one advisory review result. The base fields are shared by every
// profile; Extra carries profile-specific fields (security: source, sink,
// sanitizers_considered, reachability, attacker_prerequisites,
// evidence_limitations, attack_surface, trust_boundary, exploitability, cwe)
// without forking the schema. All extras are optional and flattened into JSON.
type Finding struct {
	ID          string     `json:"id"`
	Fingerprint string     `json:"fingerprint"`
	Severity    Severity   `json:"severity"`
	Confidence  Confidence `json:"confidence"`
	Category    Category   `json:"category"`
	Title       string     `json:"title"`

	// Primary location (mirrored as locations[0] with role "primary").
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`

	Locations      []Location `json:"locations,omitempty"`
	Evidence       string     `json:"evidence"`
	Impact         string     `json:"impact,omitempty"`
	Recommendation string     `json:"recommendation,omitempty"`

	// Verified is set by the verification pass. Unverified findings are
	// suppressed unless the caller opts into low-confidence output.
	Verified          bool   `json:"verified"`
	VerificationNotes string `json:"verification_notes,omitempty"`

	// Extra holds profile-specific fields, flattened into JSON output by
	// MarshalJSON so consumers see one flat finding object.
	Extra map[string]string `json:"-"`
}

// RunInfo describes how the report was produced.
type RunInfo struct {
	Tool             string    `json:"tool"`
	Version          string    `json:"version,omitempty"`
	Model            string    `json:"model,omitempty"`
	Commit           string    `json:"commit,omitempty"`
	Branch           string    `json:"branch,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	DurationMS       int64     `json:"duration_ms,omitempty"`
	Truncated        bool      `json:"truncated"`
	TruncationReason string    `json:"truncation_reason,omitempty"`
}

// ScopeInfo is the resolved review scope as decided by mow (not by the model).
type ScopeInfo struct {
	// Mode is how the scope was chosen: diff, staged, base, paths, worktree.
	// Consumers should branch on this rather than guessing from which of the
	// selector fields is non-empty.
	Mode string `json:"mode,omitempty"`
	// Selection is the human description of what was reviewed (a git range,
	// "uncommitted changes", the path list). Always set.
	Selection     string   `json:"selection,omitempty"`
	Paths         []string `json:"paths,omitempty"`
	Diff          string   `json:"diff,omitempty"`
	Staged        bool     `json:"staged,omitempty"`
	Base          string   `json:"base,omitempty"`
	Files         []string `json:"files,omitempty"`
	FilesReviewed int      `json:"files_reviewed"`
	FilesExcluded int      `json:"files_excluded"`
	Excluded      []string `json:"excluded,omitempty"`
	Budget        string   `json:"budget,omitempty"`
}

// Counts is the per-severity histogram of reported findings.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
	Total    int `json:"total"`
}

// Report is the top-level envelope shared by mow review and mow sec.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Profile       string    `json:"profile"`
	Advisory      bool      `json:"advisory"`
	Run           RunInfo   `json:"run"`
	Scope         ScopeInfo `json:"scope"`
	Counts        Counts    `json:"counts"`
	Findings      []Finding `json:"findings"`
	// Suppressed counts findings dropped by verification or min-severity, so a
	// quiet report still tells the reader something was filtered.
	Suppressed int      `json:"suppressed"`
	Summary    string   `json:"summary"`
	Notes      []string `json:"notes,omitempty"`
}

// NewReport returns an envelope with the invariant fields set.
func NewReport(profile string) *Report {
	return &Report{
		SchemaVersion: SchemaVersion,
		Profile:       profile,
		Advisory:      true,
		Findings:      []Finding{},
	}
}

// Fingerprint is a stable identity for a finding across runs: it deliberately
// excludes line numbers (which drift with unrelated edits) and free-form prose,
// so CI can suppress or track a known finding.
func Fingerprint(profile string, f Finding) string {
	h := sha256.New()
	for _, part := range []string{
		strings.ToLower(strings.TrimSpace(profile)),
		strings.ToLower(strings.TrimSpace(string(f.Category))),
		strings.TrimSpace(f.Path),
		normalizeTitle(f.Title),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:32]
}

// normalizeTitle lowercases and collapses whitespace/punctuation so trivial
// rewording of the same finding keeps its fingerprint.
func normalizeTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

// Recount recomputes Counts from Findings and returns the report for chaining.
func (r *Report) Recount() *Report {
	r.Counts = Counts{}
	for _, f := range r.Findings {
		switch f.Severity {
		case SevCritical:
			r.Counts.Critical++
		case SevHigh:
			r.Counts.High++
		case SevMedium:
			r.Counts.Medium++
		case SevLow:
			r.Counts.Low++
		case SevInfo:
			r.Counts.Info++
		}
	}
	r.Counts.Total = len(r.Findings)
	return r
}

// MaxSeverity returns the highest severity present (SevUnknown when empty).
func (r *Report) MaxSeverity() Severity {
	max := SevUnknown
	for _, f := range r.Findings {
		if f.Severity > max {
			max = f.Severity
		}
	}
	return max
}
