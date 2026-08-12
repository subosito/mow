package review

import (
	"fmt"
	"sort"
	"strings"
)

// Validate normalizes and checks a batch of raw findings against the resolved
// scope. It is the gate between untrusted model output and anything mow
// renders or exits on: invalid findings are dropped with a recorded reason
// rather than surfaced as free-form text.
//
// The returned findings are sorted (severity desc, then confidence desc, then
// path/line), assigned stable fingerprints, and given sequential ids.
func Validate(raw []Finding, workspace string, opt ValidationOptions) ([]Finding, []ValidationIssue) {
	prof := opt.Profile
	if prof == nil {
		prof = GeneralProfile()
	}
	var out []Finding
	var issues []ValidationIssue
	seen := map[string]int{} // fingerprint → index in out

	for i, f := range raw {
		f, err := validateOne(f, workspace, prof, opt)
		if err != nil {
			issues = append(issues, ValidationIssue{
				Index: i, Title: strings.TrimSpace(raw[i].Title),
				Reason: err.Error(), Dropped: true,
			})
			continue
		}
		f.Fingerprint = Fingerprint(prof.Name, f)
		// Same finding twice: keep the stronger severity/confidence.
		if at, dup := seen[f.Fingerprint]; dup {
			if f.Severity > out[at].Severity {
				out[at].Severity = f.Severity
			}
			if f.Confidence > out[at].Confidence {
				out[at].Confidence = f.Confidence
			}
			mergeReviewerProvenance(&out[at], f)
			issues = append(issues, ValidationIssue{
				Index: i, Title: f.Title,
				Reason: "duplicate of an earlier finding (merged)", Dropped: true,
			})
			continue
		}
		seen[f.Fingerprint] = len(out)
		out = append(out, f)
	}

	sortFindings(out)
	prefix := findingIDPrefix(prof.Name)
	for i := range out {
		out[i].ID = fmt.Sprintf("%s-%03d", prefix, i+1)
	}
	return out, issues
}

// mergeReviewerProvenance combines ensemble reviewer names when duplicate
// candidates are merged on fingerprint. The singular "reviewer" field is kept
// for backward compatibility; "reviewers" lists every model that reported it.
func mergeReviewerProvenance(dst *Finding, src Finding) {
	names := reviewerNames(*dst)
	for _, n := range reviewerNames(src) {
		if !containsReviewer(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	if dst.Extra == nil {
		dst.Extra = map[string]string{}
	}
	dst.Extra["reviewer"] = names[0]
	if len(names) > 1 {
		dst.Extra["reviewers"] = strings.Join(names, ", ")
	} else {
		// Keep singular/plural extras consistent when a merge collapses to one name.
		delete(dst.Extra, "reviewers")
	}
}

func reviewerNames(f Finding) []string {
	if f.Extra == nil {
		return nil
	}
	if rs := strings.TrimSpace(f.Extra["reviewers"]); rs != "" {
		var out []string
		for _, part := range strings.Split(rs, ",") {
			if n := strings.TrimSpace(part); n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	if r := strings.TrimSpace(f.Extra["reviewer"]); r != "" {
		return []string{r}
	}
	return nil
}

func containsReviewer(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// validateOne checks a single finding, returning the normalized copy.
func validateOne(f Finding, workspace string, prof *Profile, opt ValidationOptions) (Finding, error) {
	f.Title = clampText(f.Title)
	if f.Title == "" {
		return f, fmt.Errorf("missing title")
	}
	f.Evidence = clampText(f.Evidence)
	if f.Evidence == "" {
		return f, fmt.Errorf("missing evidence")
	}
	if !f.Severity.Valid() {
		return f, fmt.Errorf("missing or invalid severity")
	}
	if !f.Confidence.Valid() {
		return f, fmt.Errorf("missing or invalid confidence")
	}
	f.Category = NormalizeCategory(string(f.Category), prof.Categories)

	rel, err := NormalizePath(f.Path, workspace)
	if err != nil {
		return f, err
	}
	f.Path = rel

	fileLines := 0
	if opt.FileLines != nil {
		n, ok := opt.FileLines(rel)
		if !ok {
			return f, fmt.Errorf("path %q does not exist in the workspace", rel)
		}
		fileLines = n
	}
	if opt.InScope != nil && !opt.InScope(rel) {
		if !opt.AllowOutOfScope {
			return f, fmt.Errorf("path %q is outside the reviewed scope", rel)
		}
		f.VerificationNotes = appendNote(f.VerificationNotes, "path outside reviewed scope")
	}

	start, end, note := normalizeLines(f.StartLine, f.EndLine, fileLines)
	f.StartLine, f.EndLine = start, end
	if note != "" {
		f.VerificationNotes = appendNote(f.VerificationNotes, note)
	}

	f.Impact = clampText(f.Impact)
	f.Recommendation = clampText(f.Recommendation)
	f.VerificationNotes = clampText(f.VerificationNotes)
	f.Locations = normalizeLocations(f, workspace, opt)
	f.Extra = normalizeExtra(f.Extra, prof)
	return redactFinding(f), nil
}

// normalizeLocations rebuilds the location list: the primary location first,
// then any additional model-supplied locations that survive normalization.
func normalizeLocations(f Finding, workspace string, opt ValidationOptions) []Location {
	locs := []Location{{Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine, Role: "primary"}}
	for _, l := range f.Locations {
		rel, err := NormalizePath(l.Path, workspace)
		if err != nil {
			continue
		}
		fileLines := 0
		if opt.FileLines != nil {
			n, ok := opt.FileLines(rel)
			if !ok {
				continue
			}
			fileLines = n
		}
		start, end, _ := normalizeLines(l.StartLine, l.EndLine, fileLines)
		role := strings.ToLower(strings.TrimSpace(l.Role))
		if role == "" || role == "primary" {
			role = "evidence"
		}
		if rel == locs[0].Path && start == locs[0].StartLine {
			continue // already covered by the primary location
		}
		locs = append(locs, Location{
			Path: rel, StartLine: start, EndLine: end,
			Role: role, Snippet: clampText(l.Snippet),
		})
	}
	return locs
}

// normalizeExtra keeps profile-declared extra fields plus any other short
// scalar the model supplied, with keys canonicalized to snake_case.
func normalizeExtra(extra map[string]string, prof *Profile) map[string]string {
	if len(extra) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range extra {
		key := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(k, "-", "_")))
		val := clampText(v)
		if key == "" || val == "" || baseFindingKeys[key] {
			continue
		}
		out[key] = redactSecrets(val)
	}
	if len(out) == 0 {
		return nil
	}
	_ = prof // profile extra fields are documented in prompts; unknown keys still pass through
	return out
}

func appendNote(existing, note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return note
	}
	return existing + "; " + note
}

// sortFindings orders a report deterministically: worst first, then by
// location so repeated runs over unchanged code produce identical output.
func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		a, b := f[i], f[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return a.Title < b.Title
	})
}

// findingIDPrefix keeps ids readable per profile ("sec-001", "review-001").
func findingIDPrefix(profile string) string {
	switch profile {
	case "security":
		return "sec"
	case "", "general":
		return "review"
	default:
		return profile
	}
}
