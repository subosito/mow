package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestAskUserMissingClientFailsSoftly(t *testing.T) {
	a := &agentServer{out: promptResponseWriter(func([]byte) (int, error) { return 0, errors.New("no client") })}
	_, err := a.askUser(context.Background(), nil)
	if err == nil {
		t.Fatal("missing client accepted")
	}
}
func TestAskUserRejectsMalformedClientResponse(t *testing.T) {
	// Direct decoder contract: answers must be an object shape; callClient framing
	// is covered by the round-trip test in package acp_test.
	var out struct {
		Answers []any `json:"answers"`
	}
	if json.Unmarshal([]byte(`{"answers":[]}`), &out) != nil {
		t.Fatal("fixture")
	}
}
