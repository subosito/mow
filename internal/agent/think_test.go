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

// Non-ASCII text before a think tag must not corrupt the strip. earliestThinkOpen
// once indexed into strings.ToLower(s) but sliced the original s; runes whose
// lowercase differs in byte length (U+212A K→k shrinks, U+023A Ⱥ→ⱥ grows) then
// misaligned the two, corrupting adjacent content — and a grow case could slice
// past end-of-string and panic, killing Run for the whole turn.
func TestExtractThinkingNonASCIIPrefix(t *testing.T) {
	cases := []struct {
		name string
		lead string
	}{
		{"kelvin_sign", strings.Repeat("\u212A", 8)},       // lower is SHORTER
		{"latin_a_bar", strings.Repeat("\u023A", 8)},       // lower is LONGER (panic risk)
		{"latin_t_bar", strings.Repeat("\u023E", 200)},     // lower is LONGER, past-end risk
		{"turkish_dotted_I", strings.Repeat("\u0130", 40)}, // 2->3 bytes
		{"mixed", "\u212A\u023A caf\u00e9 na\u00efve \u0130"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.lead + " <think>secret</think> answer"
			vis, think, unclosed := extractThinking(in)
			if unclosed {
				t.Fatalf("unclosed=true for closed tag")
			}
			if strings.Contains(vis, "secret") {
				t.Fatalf("CoT leaked into visible: %q", vis)
			}
			if think != "secret" {
				t.Fatalf("thinking=%q want %q", think, "secret")
			}
			// The lead text must survive byte-for-byte.
			if !strings.HasPrefix(vis, tc.lead) {
				t.Fatalf("lead text corrupted:\n got %q\nwant prefix %q", vis, tc.lead)
			}
			if !strings.Contains(vis, "answer") {
				t.Fatalf("trailing content lost: %q", vis)
			}
		})
	}
}

// A fenced CoT block that quotes code must not end at the nested fence: models
// routinely include ```go ... ``` inside their reasoning, and closing early
// leaks the rest of the chain-of-thought into committed history.
func TestExtractThinkingFencedNestedCodeFence(t *testing.T) {
	in := "```thinking\nplan it:\n```go\nfoo()\n```\nthe secret plan is X.\n```\nHere is the answer."
	vis, think, _ := extractThinking(in)
	if strings.Contains(vis, "secret plan") {
		t.Fatalf("CoT leaked into committed history: %q", vis)
	}
	if !strings.Contains(think, "secret plan") {
		t.Fatalf("CoT not captured as thinking: %q", think)
	}
	if !strings.Contains(vis, "Here is the answer.") {
		t.Fatalf("answer lost: %q", vis)
	}
}
