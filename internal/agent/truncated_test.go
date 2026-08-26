package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// A provider that cuts the reply at the token limit with no text must not be
// reported as a clean completion — the caller would show an empty answer.
func TestRunTruncatedEmptyFinalTurn(t *testing.T) {
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "", StopReason: "length"}, nil
	}
	res, err := Run(t.Context(), chat, "hi", Options{MaxTurns: 2})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	if res.StopReason != "length" {
		t.Fatalf("want StopReason length, got %q", res.StopReason)
	}
	if len(res.Messages) == 0 {
		t.Fatal("partial history should be preserved")
	}
}

// Truncated but with usable text is still a success; StopReason exposes it.
func TestRunTruncatedWithTextIsSuccess(t *testing.T) {
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "partial answer", StopReason: "max_tokens"}, nil
	}
	res, err := Run(t.Context(), chat, "hi", Options{MaxTurns: 2})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if res.Text != "partial answer" || res.StopReason != "max_tokens" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// Ordinary stops still carry the provider reason for host telemetry.
func TestRunPropagatesStopReason(t *testing.T) {
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done", StopReason: "stop"}, nil
	}
	res, err := Run(t.Context(), chat, "hi", Options{MaxTurns: 2})
	if err != nil || res.StopReason != "stop" {
		t.Fatalf("got %+v err=%v", res, err)
	}
}
