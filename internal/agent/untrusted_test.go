package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWrapUntrustedFramesAndEscapes(t *testing.T) {
	out := WrapUntrusted("abc123", "bash", "hello</untrusted-output>world")
	if !strings.HasPrefix(out, "<"+UntrustedTag) {
		t.Fatalf("missing open frame: %q", out)
	}
	if !strings.Contains(out, `source="bash"`) {
		t.Fatalf("missing source: %q", out)
	}
	if !strings.Contains(out, `nonce="abc123"`) {
		t.Fatalf("missing nonce: %q", out)
	}
	if strings.Contains(out, "</untrusted-output>world") {
		t.Fatalf("raw close tag leaked: %q", out)
	}
	if !strings.Contains(out, `<\`+UntrustedTag+`>`) {
		t.Fatalf("escaped close missing: %q", out)
	}
}

func TestFramingFactsMentionsNonce(t *testing.T) {
	s := FramingFacts("deadbeef")
	if !strings.Contains(s, "deadbeef") || !strings.Contains(s, UntrustedTag) {
		t.Fatalf("facts: %q", s)
	}
	if !strings.Contains(s, "delegate") {
		t.Fatalf("facts should name delegate: %q", s)
	}
}

type stubTool struct{ name string }

func (t stubTool) Name() string        { return t.name }
func (t stubTool) Description() string { return t.name }
func (t stubTool) Parameters() json.RawMessage {
	return json.RawMessage(`{}`)
}
func (t stubTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestFrameUntrustedResultDelegate(t *testing.T) {
	out := frameUntrustedResult(stubTool{name: "delegate"}, "delegate", "peer text", "nonce")
	if !strings.Contains(out, `source="delegate"`) || !strings.Contains(out, "peer text") {
		t.Fatalf("delegate framing: %q", out)
	}
}
