package contextload_test

import (
	"strings"
	"testing"

	"github.com/subosito/mow/internal/contextload"
)

func TestDefaultHarnessRules_core(t *testing.T) {
	s := contextload.DefaultHarnessRules
	for _, want := range []string{
		"Prefer read",
		"Never discard uncommitted work",
		"Continue",
		"workspace",
	} {
		if !strings.Contains(strings.ToLower(s), strings.ToLower(want)) {
			t.Errorf("harness rules missing %q", want)
		}
	}
	// Rules block must not claim product identity (that lives in identity or prefix).
	if strings.Contains(strings.ToLower(s), "you are mow") {
		t.Error("rules must not say You are mow — that is the optional identity line")
	}
	if strings.Contains(strings.ToLower(s), "system prefix") {
		t.Error("rules must not discuss system_prefix dual-identity")
	}
	for _, ban := range []string{"chacha", "cincai", "review-hard", "dguard", "claude code"} {
		if strings.Contains(strings.ToLower(s), ban) {
			t.Errorf("harness must stay agnostic; found %q", ban)
		}
	}
}

func TestWithOptionalIdentity(t *testing.T) {
	body := contextload.ComposeSystem("AGENTS body")
	with := contextload.WithOptionalIdentity(true, body)
	without := contextload.WithOptionalIdentity(false, body)

	if !strings.HasPrefix(with, contextload.DefaultHarnessIdentity) {
		t.Fatalf("with identity: %q", with[:80])
	}
	if strings.Contains(without, "You are mow") {
		t.Fatalf("without identity still has You are mow: %q", without[:120])
	}
	if !strings.Contains(without, "AGENTS body") || !strings.Contains(with, "AGENTS body") {
		t.Fatal("missing AGENTS body")
	}
	if !strings.Contains(without, "Never discard uncommitted work") {
		t.Fatal("rules missing without identity")
	}
}

func TestComposeSystem_rulesFirst(t *testing.T) {
	got := contextload.ComposeSystem("AGENTS body")
	if !strings.HasPrefix(got, strings.TrimSpace(contextload.DefaultHarnessRules)) {
		t.Fatal("rules must come first")
	}
	if strings.HasPrefix(got, "You are mow") {
		t.Fatal("ComposeSystem must not include identity")
	}
}
