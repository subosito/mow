package review

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// SARIF 2.1.0 projection. SARIF is what GitHub code scanning, GitLab, and most
// review UIs already ingest, so emitting it is the cheapest way to make review
// output consumable by existing tooling.
const (
	sarifVersion = "2.1.0"
	sarifSchema  = "https://json.schemastore.org/sarif-2.1.0.json"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
	// Invocation carries partial-run status: a truncated review must not look
	// like a complete clean scan to the consuming UI.
	Invocations []sarifInvocation `json:"invocations,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name,omitempty"`
	ShortDescription     sarifText         `json:"shortDescription"`
	FullDescription      *sarifText        `json:"fullDescription,omitempty"`
	DefaultConfiguration *sarifRuleConfig  `json:"defaultConfiguration,omitempty"`
	Properties           map[string]string `json:"properties,omitempty"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID          string            `json:"ruleId"`
	Level           string            `json:"level"`
	Message         sarifText         `json:"message"`
	Locations       []sarifLocation   `json:"locations,omitempty"`
	RelatedLocation []sarifLocation   `json:"relatedLocations,omitempty"`
	Fingerprints    map[string]string `json:"partialFingerprints,omitempty"`
	Properties      map[string]string `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
	Message          *sarifText    `json:"message,omitempty"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine,omitempty"`
}

type sarifInvocation struct {
	ExecutionSuccessful        bool                `json:"executionSuccessful"`
	ToolExecutionNotifications []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}

type sarifNotification struct {
	Level   string    `json:"level"`
	Message sarifText `json:"message"`
}

// RenderSARIF writes the report as a SARIF 2.1.0 log.
func RenderSARIF(w io.Writer, rep *Report) error {
	if rep == nil {
		return fmt.Errorf("review: nil report")
	}
	run := sarifRun{
		Tool: sarifTool{Driver: sarifDriver{
			Name:           rep.Run.Tool,
			Version:        rep.Run.Version,
			InformationURI: "https://github.com/subosito/mow",
			Rules:          sarifRules(rep),
		}},
		Results: sarifResults(rep),
	}
	// Always record the advisory nature, and flag partial coverage explicitly.
	notes := []sarifNotification{{
		Level:   "note",
		Message: sarifText{Text: "AI-assisted advisory review. Findings are suggestions, not proof of correctness or security."},
	}}
	if rep.Run.Truncated {
		notes = append(notes, sarifNotification{
			Level:   "warning",
			Message: sarifText{Text: "Review scope was truncated: " + rep.Run.TruncationReason},
		})
	}
	run.Invocations = []sarifInvocation{{ExecutionSuccessful: true, ToolExecutionNotifications: notes}}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(sarifLog{Schema: sarifSchema, Version: sarifVersion, Runs: []sarifRun{run}})
}

// sarifRules derives one rule per category present, so a UI can group findings.
func sarifRules(rep *Report) []sarifRule {
	seen := map[Category]Severity{}
	for _, f := range rep.Findings {
		if s, ok := seen[f.Category]; !ok || f.Severity > s {
			seen[f.Category] = f.Severity
		}
	}
	cats := make([]string, 0, len(seen))
	for c := range seen {
		cats = append(cats, string(c))
	}
	sort.Strings(cats)
	rules := make([]sarifRule, 0, len(cats))
	for _, c := range cats {
		cat := Category(c)
		rules = append(rules, sarifRule{
			ID:                   sarifRuleID(rep.Profile, cat),
			Name:                 c,
			ShortDescription:     sarifText{Text: sarifRuleDescription(rep.Profile, cat)},
			DefaultConfiguration: &sarifRuleConfig{Level: sarifLevel(seen[cat])},
			Properties:           map[string]string{"profile": rep.Profile, "category": c},
		})
	}
	return rules
}

// sarifRuleID namespaces rules by profile so review and sec findings do not
// collide in a shared code-scanning dashboard.
func sarifRuleID(profile string, c Category) string {
	return "mow/" + profile + "/" + string(c)
}

func sarifRuleDescription(profile string, c Category) string {
	if profile == "security" {
		return "Security review finding: " + string(c)
	}
	return "Code review finding: " + string(c)
}

// sarifResults projects findings into SARIF results.
func sarifResults(rep *Report) []sarifResult {
	out := make([]sarifResult, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		props := map[string]string{
			"id":         f.ID,
			"confidence": f.Confidence.String(),
			"category":   string(f.Category),
			"severity":   f.Severity.String(),
			"verified":   fmt.Sprintf("%t", f.Verified),
			"advisory":   "true",
		}
		for k, v := range f.Extra {
			if _, taken := props[k]; !taken {
				props[k] = v
			}
		}
		if f.Impact != "" {
			props["impact"] = f.Impact
		}
		if f.Recommendation != "" {
			props["recommendation"] = f.Recommendation
		}
		res := sarifResult{
			RuleID:  sarifRuleID(rep.Profile, f.Category),
			Level:   sarifLevel(f.Severity),
			Message: sarifText{Text: sarifMessage(f)},
			Locations: []sarifLocation{{
				PhysicalLocation: sarifPhysical{
					ArtifactLocation: sarifArtifact{URI: f.Path},
					Region:           sarifRegionFor(f.StartLine, f.EndLine),
				},
			}},
			Fingerprints: map[string]string{"mow/v1": f.Fingerprint},
			Properties:   props,
		}
		for _, l := range f.Locations[min(1, len(f.Locations)):] {
			res.RelatedLocation = append(res.RelatedLocation, sarifLocation{
				PhysicalLocation: sarifPhysical{
					ArtifactLocation: sarifArtifact{URI: l.Path},
					Region:           sarifRegionFor(l.StartLine, l.EndLine),
				},
				Message: &sarifText{Text: l.Role},
			})
		}
		out = append(out, res)
	}
	return out
}

// sarifMessage keeps title plus evidence in the message body, since many UIs
// only show the message.
func sarifMessage(f Finding) string {
	msg := f.Title
	if f.Evidence != "" {
		msg += "\n\n" + f.Evidence
	}
	if f.Recommendation != "" {
		msg += "\n\nRecommendation: " + f.Recommendation
	}
	if !f.Verified {
		msg += "\n\n(Not confirmed by the verification pass.)"
	}
	return msg
}

// sarifRegionFor omits the region when no usable line is known: an invented
// line 0/1 would point a reviewer at the wrong code.
func sarifRegionFor(start, end int) *sarifRegion {
	if start <= 0 {
		return nil
	}
	r := &sarifRegion{StartLine: start}
	if end > start {
		r.EndLine = end
	}
	return r
}

// sarifLevel maps severity onto the SARIF level vocabulary.
func sarifLevel(s Severity) string {
	switch s {
	case SevCritical, SevHigh:
		return "error"
	case SevMedium:
		return "warning"
	case SevLow, SevInfo:
		return "note"
	default:
		return "none"
	}
}
