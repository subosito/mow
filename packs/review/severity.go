package review

import "strings"

// Severity ranks how bad a finding is if real. Ordered from Info (weakest) to
// Critical (strongest); the zero value is invalid so unset fields fail validation.
type Severity int

// Severity levels, lowest to highest.
const (
	SevUnknown Severity = iota
	SevInfo
	SevLow
	SevMedium
	SevHigh
	SevCritical
)

var severityNames = map[Severity]string{
	SevInfo:     "info",
	SevLow:      "low",
	SevMedium:   "medium",
	SevHigh:     "high",
	SevCritical: "critical",
}

// String returns the wire name ("" when unknown).
func (s Severity) String() string { return severityNames[s] }

// Valid reports whether s is a known level.
func (s Severity) Valid() bool { return s >= SevInfo && s <= SevCritical }

// ParseSeverity maps a wire name (case/space insensitive) to a Severity.
// Common model synonyms are accepted so a slightly-off label is not a hard error.
func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "crit", "blocker":
		return SevCritical, true
	case "high", "major", "error":
		return SevHigh, true
	case "medium", "moderate", "warning", "warn":
		return SevMedium, true
	case "low", "minor":
		return SevLow, true
	case "info", "informational", "note", "nit", "nitpick":
		return SevInfo, true
	}
	return SevUnknown, false
}

// MarshalJSON writes the wire name.
func (s Severity) MarshalJSON() ([]byte, error) { return marshalEnum(s.String()) }

// UnmarshalJSON accepts a wire name or synonym.
func (s *Severity) UnmarshalJSON(b []byte) error {
	raw, err := unmarshalEnum(b)
	if err != nil {
		return err
	}
	v, ok := ParseSeverity(raw)
	if !ok {
		return errEnum("severity", raw, SeverityNames())
	}
	*s = v
	return nil
}

// SeverityNames lists valid severity wire names, weakest first.
func SeverityNames() []string {
	return []string{"info", "low", "medium", "high", "critical"}
}

// Confidence is how sure the reviewer is that a finding is real (not how bad it is).
type Confidence int

// Confidence levels, lowest to highest.
const (
	ConfUnknown Confidence = iota
	ConfLow
	ConfMedium
	ConfHigh
)

var confidenceNames = map[Confidence]string{
	ConfLow:    "low",
	ConfMedium: "medium",
	ConfHigh:   "high",
}

// String returns the wire name ("" when unknown).
func (c Confidence) String() string { return confidenceNames[c] }

// Valid reports whether c is a known level.
func (c Confidence) Valid() bool { return c >= ConfLow && c <= ConfHigh }

// ParseConfidence maps a wire name (case/space insensitive) to a Confidence.
func ParseConfidence(s string) (Confidence, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high", "certain", "confirmed":
		return ConfHigh, true
	case "medium", "moderate", "likely":
		return ConfMedium, true
	case "low", "speculative", "possible", "unsure":
		return ConfLow, true
	}
	return ConfUnknown, false
}

// MarshalJSON writes the wire name.
func (c Confidence) MarshalJSON() ([]byte, error) { return marshalEnum(c.String()) }

// UnmarshalJSON accepts a wire name or synonym.
func (c *Confidence) UnmarshalJSON(b []byte) error {
	raw, err := unmarshalEnum(b)
	if err != nil {
		return err
	}
	v, ok := ParseConfidence(raw)
	if !ok {
		return errEnum("confidence", raw, ConfidenceNames())
	}
	*c = v
	return nil
}

// ConfidenceNames lists valid confidence wire names, weakest first.
func ConfidenceNames() []string { return []string{"low", "medium", "high"} }
