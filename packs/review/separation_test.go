package review

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The two commands are separate products that happen to share an engine. These
// tests pin the boundary: `mow review` must never run the security persona,
// and `mow sec` findings must stay distinguishable from review findings all
// the way out to the machine-readable formats.
//
// Everything here is about the *seam*, not about either persona's quality.

// A security review must be reachable only through `sec`. If a flag, an env
// var, or a config key ever selects the persona, this test is where it should
// fail: the CLIFlags surface is the complete user-facing knob set, and none of
// it may name a profile.
func TestProfileNotSelectableByFlag(t *testing.T) {
	// Every flag the user can pass to `mow review`, with a value. If any of
	// these could reach the security persona, the general command would be
	// silently running an adversarial review.
	args := [][]string{
		{"--budget", "large"},
		{"--min-severity", "critical"},
		{"--include-unverified"},
		{"--no-verify"},
		{"--include-low"},
		{"--staged"},
	}
	for _, a := range args {
		t.Run(strings.Join(a, " "), func(t *testing.T) {
			rf, paths, err := parseFlags(t, "review", a...)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			req, _, _, err := rf.Resolve(GeneralProfile(), "/ws", paths)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if req.Profile.Name != "general" {
				t.Errorf("profile = %q, want general — a flag reached the persona", req.Profile.Name)
			}
		})
	}

	// And the inverse: a `--profile` style flag must not exist at all. If
	// someone adds one, parsing it will succeed and this test fails.
	if _, _, err := parseFlags(t, "review", "--profile", "security"); err == nil {
		t.Error("--profile parsed; the persona must be chosen by the command, not a flag")
	}
	if _, _, err := parseFlags(t, "review", "--sec"); err == nil {
		t.Error("--sec parsed; use the sec command instead")
	}
}

// The command name is the only thing that picks a persona, in every entry
// point. A new surface that forgets this is the realistic regression.
func TestCommandSelectsProfile(t *testing.T) {
	tests := []struct {
		cmd         string
		wantProfile string
		wantPrefix  string
	}{
		{"review", "general", "review"},
		{"sec", "security", "sec"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			prof, err := profileFor(tt.cmd)
			if err != nil {
				t.Fatalf("profileFor: %v", err)
			}
			if prof.Name != tt.wantProfile {
				t.Errorf("profile = %q, want %q", prof.Name, tt.wantProfile)
			}
			if got := findingIDPrefix(prof.Name); got != tt.wantPrefix {
				t.Errorf("id prefix = %q, want %q", got, tt.wantPrefix)
			}
		})
	}

	// An unknown command must fail rather than default to general. A future
	// `mow audit` that silently ran the general review would be a security
	// command quietly doing a non-security job.
	if _, err := profileFor("audit"); err == nil {
		t.Error("unknown command defaulted to a profile instead of failing")
	}
}

// The personas must differ in the ways users actually depend on: the prompt
// they send, the taxonomy they accept, and the noise floor.
func TestProfilesAreDistinct(t *testing.T) {
	gen, sec := GeneralProfile(), SecurityProfile()

	if gen.Name == sec.Name || gen.Command == sec.Command {
		t.Fatal("profiles share identity")
	}
	// `mow sec` defaults to a higher floor: an adversarial read produces more
	// speculative low-severity noise than a correctness read.
	if sec.MinSeverity <= gen.MinSeverity {
		t.Errorf("sec MinSeverity %v <= review %v; sec should have the higher floor",
			sec.MinSeverity, gen.MinSeverity)
	}

	genPrompt := systemPrompt(gen)
	secPrompt := systemPrompt(sec)
	if genPrompt == secPrompt {
		t.Fatal("identical system prompts — the personas are not actually distinct")
	}
	// The adversarial framing is the product difference; assert it is present
	// in sec and absent from review rather than comparing whole strings.
	for _, want := range []string{"security", "attacker"} {
		if !strings.Contains(strings.ToLower(secPrompt), want) {
			t.Errorf("sec prompt missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(genPrompt), "attacker") {
		t.Error("review prompt mentions attackers; that is sec's job")
	}
}

// Taxonomies must not bleed. A security category returned during a general
// review has to normalize to "other" rather than being accepted, or `mow
// review` output would carry security labels it never reasoned about.
func TestTaxonomiesDoNotBleed(t *testing.T) {
	gen, sec := GeneralProfile(), SecurityProfile()

	secOnly := map[Category]bool{}
	for _, c := range sec.Categories {
		secOnly[c] = true
	}
	for _, c := range gen.Categories {
		delete(secOnly, c)
	}
	if len(secOnly) == 0 {
		t.Fatal("no security-only categories; the taxonomies are not distinct")
	}

	for c := range secOnly {
		got := NormalizeCategory(string(c), gen.Categories)
		if got == c {
			t.Errorf("security category %q survived a general review", c)
		}
		if got != CatOther {
			t.Errorf("NormalizeCategory(%q, general) = %q, want %q", c, got, CatOther)
		}
		// And it must survive its own profile, or sec would lose its taxonomy.
		if got := NormalizeCategory(string(c), sec.Categories); got != c {
			t.Errorf("NormalizeCategory(%q, security) = %q, want it preserved", c, got)
		}
	}
}

// Two findings that are identical except for the profile must not collide.
// They land in the same code-scanning dashboard, and a shared fingerprint
// would let a review finding dismiss its security counterpart.
func TestFingerprintSeparatesProfiles(t *testing.T) {
	f := Finding{Category: CatOther, Path: "a.go", Title: "same title"}
	if Fingerprint("general", f) == Fingerprint("security", f) {
		t.Error("review and sec findings share a fingerprint")
	}
}

// SARIF is a generic interchange format, not a security format — it is what
// GitHub code scanning and friends ingest, and it carries correctness findings
// just as well as vulnerabilities. So `mow review --format sarif` is correct
// and is *not* a security report.
//
// What must hold is that a consumer can tell the two apart. Both land in one
// dashboard, so rule ids are namespaced by profile: without that, a review
// "other" finding and a sec "other" finding would share a rule, and dismissing
// one would dismiss the other.
func TestSARIFNamespacesRulesByProfile(t *testing.T) {
	sarifFor := func(profile string) map[string]any {
		rep := &Report{
			Profile: profile,
			Run:     RunInfo{Tool: "mow", Version: "test"},
			Findings: []Finding{{
				ID: "x-1", Category: CatOther, Path: "a.go",
				StartLine: 10, Title: "same title", Severity: SevHigh,
			}},
		}
		var b bytes.Buffer
		if err := RenderSARIF(&b, rep); err != nil {
			t.Fatalf("RenderSARIF(%s): %v", profile, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b.Bytes(), &m); err != nil {
			t.Fatalf("SARIF is not valid JSON: %v", err)
		}
		return m
	}

	ruleID := func(m map[string]any) string {
		run := m["runs"].([]any)[0].(map[string]any)
		return run["results"].([]any)[0].(map[string]any)["ruleId"].(string)
	}

	gen, sec := sarifFor("general"), sarifFor("security")
	genID, secID := ruleID(gen), ruleID(sec)

	if genID == secID {
		t.Fatalf("both profiles emit ruleId %q; findings would collide in a shared dashboard", genID)
	}
	if !strings.Contains(genID, "general") {
		t.Errorf("review ruleId %q does not name its profile", genID)
	}
	if !strings.Contains(secID, "security") {
		t.Errorf("sec ruleId %q does not name its profile", secID)
	}

	// The advisory notice rides along on every run regardless of profile: a
	// SARIF consumer must never read either report as an authoritative scan.
	run := gen["runs"].([]any)[0].(map[string]any)
	inv := run["invocations"].([]any)[0].(map[string]any)
	notes, _ := json.Marshal(inv["toolExecutionNotifications"])
	if !strings.Contains(strings.ToLower(string(notes)), "advisory") {
		t.Errorf("SARIF run omits the advisory notice: %s", notes)
	}
}
