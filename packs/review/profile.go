package review

// Profile is an internal review persona: taxonomy, defaults, and prompt text.
// Users pick a command (`mow review` / `mow sec`), not a profile flag. Profile
// still appears in report JSON/SARIF as provenance so machines can tell the
// two products apart. Library callers pass *Profile on Request.
type Profile struct {
	// Name is the wire value written to report.profile ("general", "security").
	Name string
	// Command is the user-facing entry point shown in reports and help.
	Command string
	// Headline is the one-line description used in text output.
	Headline string
	// Categories is the allowed taxonomy; unknown labels normalize to "other".
	Categories []Category
	// MinSeverity is the default floor for reported findings.
	MinSeverity Severity
	// FailOn is the default severity that makes the command exit non-zero.
	FailOn Severity
	// ExtraFields are optional profile-specific finding fields the model may
	// return; they are preserved in Finding.Extra and rendered when present.
	ExtraFields []string
}

// GeneralProfile is the default code-review persona behind `mow review`.
func GeneralProfile() *Profile {
	return &Profile{
		Name:        "general",
		Command:     "mow review",
		Headline:    "AI-assisted code review. Findings are advisory.",
		Categories:  append([]Category(nil), generalCategories...),
		MinSeverity: SevLow,
		FailOn:      SevHigh,
		ExtraFields: []string{"affected_behavior", "test_gap"},
	}
}

// SecurityProfile is the adversarial persona behind `mow sec`.
func SecurityProfile() *Profile {
	return &Profile{
		Name:        "security",
		Command:     "mow sec",
		Headline:    "AI-assisted security review. Findings are advisory, not a proof of security.",
		Categories:  append([]Category(nil), securityCategories...),
		MinSeverity: SevMedium,
		FailOn:      SevHigh,
		ExtraFields: []string{"attack_surface", "trust_boundary", "exploitability", "cwe"},
	}
}

// Profiles returns the built-in profiles keyed by name.
func Profiles() map[string]*Profile {
	return map[string]*Profile{
		"general":  GeneralProfile(),
		"security": SecurityProfile(),
	}
}

// LookupProfile resolves a profile by name (empty name → general).
func LookupProfile(name string) (*Profile, bool) {
	if name == "" {
		return GeneralProfile(), true
	}
	p, ok := Profiles()[name]
	return p, ok
}

// ProfileNames lists built-in profile names in a stable order.
func ProfileNames() []string { return []string{"general", "security"} }
