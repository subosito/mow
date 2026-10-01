package acp

import (
	"slices"
	"testing"
)

func TestPeerEnvDropsPaneOwnerVars(t *testing.T) {
	parent := []string{"PATH=/bin", "HERDR_ENV=1", "HERDR_PANE_ID=p1", "OPENAI_API_KEY=k"}
	got := peerEnv(parent, []string{"ANTHROPIC_MODEL=x"})
	want := []string{"PATH=/bin", "OPENAI_API_KEY=k", "ANTHROPIC_MODEL=x"}
	if !slices.Equal(got, want) {
		t.Fatalf("peerEnv = %v, want %v", got, want)
	}
	// Before this change a peer with no configured env inherited the raw
	// parent environment, pane binding included.
	if got := peerEnv(parent, nil); slices.ContainsFunc(got, func(kv string) bool { return kv == "HERDR_PANE_ID=p1" }) {
		t.Fatalf("pane id reached a peer: %v", got)
	}
}
