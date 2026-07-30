package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNoJSON means the model reply contained no JSON object at all. Callers
// treat this as a tooling failure, never as "the code is clean".
var ErrNoJSON = errors.New("review: model reply contained no JSON object")

// candidateEnvelope is the strict contract for the candidate pass.
type candidateEnvelope struct {
	Findings []Finding `json:"findings"`
	Notes    []string  `json:"notes,omitempty"`
	Summary  string    `json:"summary,omitempty"`
}

// verdictEnvelope is the strict contract for the verification pass. The
// verifier answers per candidate id rather than restating whole findings, so it
// cannot smuggle in new claims or silently rewrite evidence.
type verdictEnvelope struct {
	Verdicts []Verdict `json:"verdicts"`
	Summary  string    `json:"summary,omitempty"`
	Notes    []string  `json:"notes,omitempty"`
}

// Verdict is the verification pass's ruling on one candidate finding.
type Verdict struct {
	ID string `json:"id"`
	// Status is "confirmed", "rejected", or "uncertain".
	Status string `json:"status"`
	// Severity optionally corrects the candidate's severity.
	Severity string `json:"severity,omitempty"`
	// Confidence optionally corrects the candidate's confidence.
	Confidence string `json:"confidence,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Confirmed reports whether the verdict keeps the finding.
func (v Verdict) Confirmed() bool {
	return strings.EqualFold(strings.TrimSpace(v.Status), "confirmed")
}

// Rejected reports whether the verdict drops the finding outright.
func (v Verdict) Rejected() bool {
	return strings.EqualFold(strings.TrimSpace(v.Status), "rejected")
}

// ExtractJSONObject pulls the JSON object out of a model reply. Models wrap
// JSON in prose or code fences even when told not to, so we locate the outermost
// balanced {...} (string- and escape-aware) instead of trusting the whole reply.
func ExtractJSONObject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrNoJSON
	}
	// Prefer a fenced block when present: prose around it may contain braces.
	if fenced, ok := fencedJSON(s); ok {
		if obj, err := balancedObject(fenced); err == nil {
			return obj, nil
		}
	}
	return balancedObject(s)
}

// fencedJSON returns the contents of the first ``` fence, if any.
func fencedJSON(s string) (string, bool) {
	i := strings.Index(s, "```")
	if i < 0 {
		return "", false
	}
	rest := s[i+3:]
	// Skip an optional language tag on the fence line.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		tag := strings.TrimSpace(rest[:nl])
		if tag == "" || !strings.ContainsAny(tag, "{}\"") {
			rest = rest[nl+1:]
		}
	}
	if j := strings.Index(rest, "```"); j >= 0 {
		return strings.TrimSpace(rest[:j]), true
	}
	return strings.TrimSpace(rest), true
}

// balancedObject scans for the first complete brace-balanced object, ignoring
// braces inside JSON strings.
func balancedObject(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", ErrNoJSON
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("review: unterminated JSON object in model reply")
}

// parseCandidates decodes the candidate-pass contract.
func parseCandidates(reply string) (candidateEnvelope, error) {
	var env candidateEnvelope
	obj, err := ExtractJSONObject(reply)
	if err != nil {
		return env, err
	}
	dec := json.NewDecoder(strings.NewReader(obj))
	if err := dec.Decode(&env); err != nil {
		return env, fmt.Errorf("review: candidate JSON did not match the contract: %w", err)
	}
	return env, nil
}

// parseVerdicts decodes the verification-pass contract.
func parseVerdicts(reply string) (verdictEnvelope, error) {
	var env verdictEnvelope
	obj, err := ExtractJSONObject(reply)
	if err != nil {
		return env, err
	}
	if err := json.Unmarshal([]byte(obj), &env); err != nil {
		return env, fmt.Errorf("review: verdict JSON did not match the contract: %w", err)
	}
	return env, nil
}
