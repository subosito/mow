package agent

import (
	"context"
	"github.com/subosito/mow/internal/llm"
	"strings"
	"testing"
)

func TestExtractThinkingComplete(t *testing.T) {
	vis, th, unclosed := extractThinking("before <think>secret plan</think> after")
	if unclosed {
		t.Fatal("should be closed")
	}
	if !strings.Contains(vis, "before") || !strings.Contains(vis, "after") {
		t.Fatalf("vis=%q", vis)
	}
	if strings.Contains(vis, "secret") {
		t.Fatalf("thinking leaked into visible: %q", vis)
	}
	if th != "secret plan" {
		t.Fatalf("th=%q", th)
	}
}

func TestExtractThinkingUnclosed(t *testing.T) {
	// Streaming: open tag without close hides remainder.
	vis, th, unclosed := extractThinking("hi <think>still going")
	if !unclosed {
		t.Fatal("expected unclosed")
	}
	if vis != "hi " {
		t.Fatalf("vis=%q", vis)
	}
	if th != "still going" {
		t.Fatalf("th=%q", th)
	}
}

func TestExtractThinkingCaseInsensitive(t *testing.T) {
	vis, th, unclosed := extractThinking("<THINK>AbC</Think>done")
	if unclosed || th != "AbC" || vis != "done" {
		t.Fatalf("vis=%q th=%q unclosed=%v", vis, th, unclosed)
	}
}

func TestExtractThinkingVariants(t *testing.T) {
	vis, th, unclosed := extractThinking("<thinking>x</thinking>y")
	if unclosed || vis != "y" || th != "x" {
		t.Fatalf("vis=%q th=%q unclosed=%v", vis, th, unclosed)
	}
}

func TestLoopStripsInlineThinking(t *testing.T) {
	// The committed turn (history + Result.Text) must be tag-free even when
	// the model wraps CoT in content instead of the reasoning channel.
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant",
			Content: "<think>secret plan</think>the answer"}, nil
	}
	res, err := Run(context.Background(), chat, "q", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the answer" {
		t.Fatalf("text=%q", res.Text)
	}
	for _, m := range res.Messages {
		if strings.Contains(m.Content, "secret plan") {
			t.Fatalf("CoT leaked into history: %q", m.Content)
		}
	}
}

func TestExtractThinkingSeamNotGlued(t *testing.T) {
	cases := []struct{ in, wantVis string }{
		// Inline tags with no surrounding whitespace must not weld prose.
		{"key files.<think>plan</think>Let me go", "key files. Let me go"},
		// Existing whitespace on either side: no extra separator injected.
		{"key files.\n<think>plan</think>\nLet me go", "key files.\nLet me go"},
		{"a <think>x</think>b", "a b"},
		{"a<think>x</think> b", "a b"},
		// Multiple blocks.
		{"one<think>x</think>two<think>y</think>three", "one two three"},
	}
	for _, c := range cases {
		vis, _, unclosed := extractThinking(c.in)
		if unclosed || vis != c.wantVis {
			t.Errorf("extractThinking(%q) vis=%q want %q (unclosed=%v)", c.in, vis, c.wantVis, unclosed)
		}
	}
}
