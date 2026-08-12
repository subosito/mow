package review

import "strings"

// securityEvidenceLevel classifies the strength of a read-only security claim.
// It deliberately does not claim execution proof: mow sec never runs the code.
// model-verified requires pass-two confirmation (Finding.Verified); complete-
// looking pass-one fields alone are capped at code-supported.
func securityEvidenceLevel(f Finding) string {
	if f.Extra == nil {
		return "suspected"
	}
	source := strings.TrimSpace(f.Extra["source"])
	sink := strings.TrimSpace(f.Extra["sink"])
	reach := strings.ToLower(strings.TrimSpace(f.Extra["reachability"]))
	limits := strings.TrimSpace(f.Extra["evidence_limitations"])
	complete := source != "" && sink != "" && reach != "" && !strings.Contains(reach, "unknown") && limits == ""
	if complete && f.Verified {
		return "model-verified"
	}
	if source != "" || sink != "" || reach != "" {
		return "code-supported"
	}
	return "suspected"
}

// applySecurityEvidenceLevel adds an optional, backward-compatible provenance
// field. It is advisory and must never be represented as execution-verified.
func applySecurityEvidenceLevel(profile string, findings []Finding) {
	if profile != "security" {
		return
	}
	for i := range findings {
		if findings[i].Extra == nil {
			findings[i].Extra = map[string]string{}
		}
		findings[i].Extra["evidence_level"] = securityEvidenceLevel(findings[i])
	}
}
