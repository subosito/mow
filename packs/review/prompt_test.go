package review

import (
	"strings"
	"testing"
)

func TestSecurityEvidenceFieldsAreProfileExtras(t *testing.T) {
	sec := SecurityProfile()
	want := []string{
		"source", "sink", "sanitizers_considered", "reachability",
		"attacker_prerequisites", "evidence_limitations",
		"attack_surface", "trust_boundary", "exploitability", "cwe",
	}
	if len(sec.ExtraFields) != len(want) {
		t.Fatalf("ExtraFields = %v, want %v", sec.ExtraFields, want)
	}
	for i, k := range want {
		if sec.ExtraFields[i] != k {
			t.Errorf("ExtraFields[%d] = %q want %q", i, sec.ExtraFields[i], k)
		}
		if SecurityEvidenceFields[i] != k {
			t.Errorf("SecurityEvidenceFields[%d] = %q want %q", i, SecurityEvidenceFields[i], k)
		}
	}
	// General review must not require or advertise the security evidence keys.
	gen := GeneralProfile()
	for _, k := range []string{"source", "sink", "sanitizers_considered", "reachability"} {
		for _, g := range gen.ExtraFields {
			if g == k {
				t.Errorf("general ExtraFields must not include security key %q", k)
			}
		}
	}
}

func TestSecurityCandidateContractTracesSourceSink(t *testing.T) {
	sec := candidateContract(SecurityProfile())
	for _, want := range []string{
		`"source"`, `"sink"`, `"sanitizers_considered"`, `"reachability"`,
		`"attacker_prerequisites"`, `"evidence_limitations"`,
		"source → transform → sink",
		"framework/upstream",
		"high confidence means",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("security candidate contract missing %q", want)
		}
	}
	gen := candidateContract(GeneralProfile())
	for _, banned := range []string{`"source"`, `"sink"`, `"sanitizers_considered"`, "attacker_prerequisites"} {
		if strings.Contains(gen, banned) {
			t.Errorf("general candidate contract should not include security field %q", banned)
		}
	}
}

func TestSecuritySystemPromptEvidenceRules(t *testing.T) {
	sec := systemPrompt(SecurityProfile())
	for _, want := range []string{
		"source → transform → sink",
		"framework",
		"model-verified",
		"suspected",
		"static advisory",
	} {
		if !strings.Contains(strings.ToLower(sec), strings.ToLower(want)) {
			t.Errorf("security system prompt missing %q", want)
		}
	}
	gen := systemPrompt(GeneralProfile())
	if strings.Contains(gen, "source → transform → sink") {
		t.Error("general system prompt should not carry security evidence rules")
	}
}

func TestSecurityCandidatePromptAndTaxonomy(t *testing.T) {
	sc := &Scope{
		Files: []ScopeFile{{Path: "internal/api/users.go", Content: "1| package api\n"}},
	}
	// Minimal index so scope content can render if needed.
	sc.index = map[string]int{"internal/api/users.go": 0}
	p := candidatePrompt(SecurityProfile(), sc)
	for _, want := range []string{
		"Pass 1 of 2",
		"source→transform→sink",
		"source",
		"sink",
		"sanitizers_considered",
		"evidence_limitations",
		"Field meanings",
		"attacker-controlled entry",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("security candidate prompt missing %q", want)
		}
	}
}

func TestSecurityVerifyPromptChallengesEvidence(t *testing.T) {
	cands := []Finding{{
		ID: "sec-001", Severity: SevHigh, Confidence: ConfMedium, Category: CatAuthz,
		Title: "Missing authz", Path: "internal/api/users.go", StartLine: 10,
		Evidence: "no ownership check",
		Extra: map[string]string{
			"source":                 "path id",
			"sink":                   "UPDATE users",
			"sanitizers_considered":  "none found",
			"reachability":           "reachable",
			"attacker_prerequisites": "session",
			"evidence_limitations":   "middleware not fully read",
		},
	}}
	p := verifyPrompt(SecurityProfile(), &Scope{}, cands)
	for _, want := range []string{
		"Pass 2 of 2",
		"source→transform→sink",
		"sanitizers/guards",
		"framework",
		"model-verified",
		"suspected",
		"source: path id",
		"sink: UPDATE users",
		"sanitizers_considered: none found",
		"reachability: reachable",
		"attacker_prerequisites: session",
		"evidence_limitations: middleware not fully read",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("security verify prompt missing %q", want)
		}
	}
	// Digests must not reintroduce the pass-1 JSON contract shape.
	if strings.Contains(p, `"findings"`) {
		t.Error("verify prompt should present digests, not the candidate JSON contract")
	}
}

func TestCandidateDigestOrdersSecurityEvidence(t *testing.T) {
	f := Finding{
		ID: "sec-001", Severity: SevHigh, Confidence: ConfHigh, Category: CatInjection,
		Title: "SQL injection", Path: "a.go", StartLine: 1,
		Evidence: "query built with +",
		Extra: map[string]string{
			"cwe":                    "CWE-89",
			"sink":                   "db.Query",
			"source":                 "r.FormValue",
			"reviewer":               "gpt-test", // non-preferred key sorts after
			"sanitizers_considered":  "none found",
			"reachability":           "reachable",
			"attacker_prerequisites": "none",
			"evidence_limitations":   "no integration test",
		},
		Locations: []Location{
			{Path: "a.go", StartLine: 1, Role: "primary"},
			{Path: "b.go", StartLine: 4, Role: "source"},
		},
	}
	d := candidateDigest(f)
	// Preferred narrative order.
	order := []string{
		"source: r.FormValue",
		"sink: db.Query",
		"sanitizers_considered: none found",
		"reachability: reachable",
		"attacker_prerequisites: none",
		"evidence_limitations: no integration test",
		"cwe: CWE-89",
		"reviewer: gpt-test",
		"related (source): b.go:4",
	}
	prev := -1
	for _, want := range order {
		i := strings.Index(d, want)
		if i < 0 {
			t.Errorf("digest missing %q\n%s", want, d)
			continue
		}
		if i < prev {
			t.Errorf("digest order wrong: %q appeared before previous field\n%s", want, d)
		}
		prev = i
	}
}

func TestOrderedExtraKeysPreferredThenSorted(t *testing.T) {
	extra := map[string]string{
		"cwe":    "CWE-22",
		"source": "input",
		"zzz":    "tail",
		"aaa":    "head",
		"sink":   "os.Open",
		"blank":  "  ",
	}
	keys := orderedExtraKeys(extra, SecurityEvidenceFields)
	want := []string{"source", "sink", "cwe", "aaa", "zzz"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v want %v", keys, want)
		}
	}
}
