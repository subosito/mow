package mowi

import (
	"strings"
	"testing"
)

// Dialect extraction lives in mow (internal/agent/think.go). These tests only
// cover mowi live-stream handling that builds on that API.

func TestApplyStreamSnapStripsThinkTags(t *testing.T) {
	m := &model{}
	m.applyStreamSnap("<think>plan details</think>## Hello", "")
	if strings.Contains(m.streamBuf, "plan") {
		t.Fatalf("streamBuf has thinking: %q", m.streamBuf)
	}
	if !strings.Contains(m.streamBuf, "Hello") {
		t.Fatalf("streamBuf missing answer: %q", m.streamBuf)
	}
	if !m.reasoningArmed() {
		t.Fatal("expected thinking armed after <think> strip")
	}
	if m.reasonBuf != "." {
		t.Fatalf("reasonBuf should be presence marker, got %q", m.reasonBuf)
	}
}

func TestApplyStreamSnapUnclosedHidesAnswer(t *testing.T) {
	m := &model{}
	// CoT starts mid-stream after a few plain tokens — hide everything until close.
	m.applyStreamSnap("project.", "")
	if m.streamBuf != "project." {
		t.Fatalf("pre-tag content: %q", m.streamBuf)
	}
	m.applyStreamSnap("<think>Let me reason without spaces", "")
	if m.streamBuf != "" {
		t.Fatalf("while unclosed think, answer must be empty (indicator only), got %q", m.streamBuf)
	}
	if !m.reasoningArmed() {
		t.Fatal("expected armed")
	}
	m.applyStreamSnap(" more</think>\nFinal answer.", "")
	if strings.Contains(m.streamBuf, "Let me") || strings.Contains(m.streamBuf, "reason") {
		t.Fatalf("CoT leaked: %q", m.streamBuf)
	}
	if !strings.Contains(m.streamBuf, "Final answer") {
		t.Fatalf("missing final answer: %q", m.streamBuf)
	}
	// Pre-tag "project." is still part of visible (outside think). Acceptable.
}

func TestApplyStreamSnapReasoningNeverInFrame(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	raw.applyStreamSnap("", "project.Let me glue tokens together from reasoning channel")
	raw.paintLiveStream()
	if strings.Contains(raw.streamFrame, "project") || strings.Contains(raw.streamFrame, "Let me") {
		t.Fatalf("reasoning channel leaked into frame: %q", raw.streamFrame)
	}
	if !strings.Contains(raw.streamFrame, "thinking") {
		t.Fatalf("want indicator: %q", raw.streamFrame)
	}
}

func TestThinkingIndicatorOnlyNoBody(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true
	mod, _ := raw.Update(reasoningMsg("secret plan that must stay hidden"))
	m := mod.(*model)
	if !strings.Contains(m.streamFrame, "thinking") {
		t.Fatalf("indicator missing: %q", m.streamFrame)
	}
	if strings.Contains(m.streamFrame, "secret plan") {
		t.Fatalf("body must never paint: %q", m.streamFrame)
	}
	mod, _ = m.Update(reasoningMsg("project.Let me continue without spaces"))
	m = mod.(*model)
	if strings.Contains(m.streamFrame, "project") || strings.Contains(m.streamFrame, "Let me") {
		t.Fatalf("reasoning tokens leaked into frame: %q", m.streamFrame)
	}
}
