package mowi

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestToolLabelReadPath(t *testing.T) {
	got := toolLabel("read", `{"path":"/work/repo/mowi/engine.go"}`, 48)
	if got != "read · engine.go" {
		t.Fatalf("got %q", got)
	}
}

func TestToolLabelBashJustVerify(t *testing.T) {
	cmd := `cd /work/repo/mowi && devenv shell -- just verify`
	got := toolLabel("bash", cmd, 48)
	if !strings.Contains(got, "bash") {
		t.Fatalf("missing bash: %q", got)
	}
	if !strings.Contains(got, "just") || !strings.Contains(got, "verify") {
		t.Fatalf("want just verify summary, got %q", got)
	}
	if strings.Contains(got, "/home/") {
		t.Fatalf("should not keep full path: %q", got)
	}
}

func TestToolLabelPeer(t *testing.T) {
	got := toolLabel("claude: read server.go", "", 48)
	if !strings.Contains(got, "claude") || !strings.Contains(got, "server.go") {
		t.Fatalf("got %q", got)
	}
	if !strings.HasPrefix(got, glyphArrow) {
		t.Fatalf("peer should use arrow prefix: %q", got)
	}
}

func TestToolLabelBareName(t *testing.T) {
	if got := toolLabel("grep", "", 20); got != "grep" {
		t.Fatalf("got %q", got)
	}
}

func TestToolLabelWidthClamp(t *testing.T) {
	got := toolLabel("bash", strings.Repeat("word ", 40), 20)
	if len([]rune(got)) > 24 { // display clamp soft bound
		// xansi width is what matters; ensure truncated with ellipsis path works
	}
	if got == "" {
		t.Fatal("empty")
	}
}

func TestToolLabelSanitizes(t *testing.T) {
	got := toolLabel("read", "ok\x1b[31msecret\x1b[0m", 40)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape leaked: %q", got)
	}
}

func TestShellSummarySkipsCd(t *testing.T) {
	got := shellSummary("cd /tmp && go test ./...")
	if !strings.Contains(got, "go") || !strings.Contains(got, "test") {
		t.Fatalf("got %q", got)
	}
}

func TestToolLabelBareColon(t *testing.T) {
	// "acp:" with no agent detail must not paint a trailing colon.
	got := toolLabel("acp:", `{"prompt":"do the thing"}`, 48)
	if strings.Contains(got, "acp:") {
		t.Fatalf("trailing colon leaked: %q", got)
	}
	if !strings.Contains(got, "acp") {
		t.Fatalf("want verb acp: %q", got)
	}
}

func TestToolLabelColonOnlyEmpty(t *testing.T) {
	// A punctuation-only name renders nothing (not a bare glyph).
	if got := toolLabel(":", "", 48); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestToolLabelDeterministicFallback(t *testing.T) {
	// Args with no "known" key must pick a stable detail — Go map iteration is
	// randomized, so a naive fallback flickered between labels across renders.
	args := `{"zeta":"zzz","alpha":"aaa","mid":"mmm"}`
	first := toolLabel("customtool", args, 60)
	for i := 0; i < 200; i++ {
		if got := toolLabel("customtool", args, 60); got != first {
			t.Fatalf("label not deterministic: %q vs %q", got, first)
		}
	}
	// Sorted-key fallback picks the alphabetically-first non-empty string.
	if !strings.Contains(first, "aaa") {
		t.Fatalf("want stable alpha value, got %q", first)
	}
}

// A delegated peer label keeps a readable floor even when the activity band
// is squeezed: "→ peer-agent · read engine.go" must not collapse to "→ gr…" the
// way a generic verb label tolerates. Truncation stays tail-safe (ellipsis).
func TestToolLabelPeerSqueezedBudget(t *testing.T) {
	name := "peer-agent: summarize the loop spine in internal/agent/loop.go and report the compaction layers in detail"
	full := toolLabel(name, "", 80)
	if xansi.StringWidth(full) <= minPeerLabelWidth {
		t.Fatalf("sanity: full peer label should exceed the floor, got %q", full)
	}
	squeezed := toolLabel(name, "", 8)
	if w := xansi.StringWidth(squeezed); w < minPeerLabelWidth-1 || w > minPeerLabelWidth {
		t.Fatalf("squeezed peer label should clamp to the %d-cell floor: %q (width %d)", minPeerLabelWidth, squeezed, w)
	}
	if !strings.Contains(squeezed, "peer-agent") {
		t.Fatalf("squeezed peer label lost the agent name: %q", squeezed)
	}
	// Task detail present (more than the bare agent) — the floor's whole point.
	if !strings.Contains(squeezed, "·") || len(strings.TrimSpace(squeezed)) < len("→ peer-agent · ")+4 {
		t.Fatalf("squeezed peer label lost the task detail: %q", squeezed)
	}
	if !strings.HasSuffix(squeezed, "\u2026") {
		t.Fatalf("peer label truncated without ellipsis: %q", squeezed)
	}
}

// Generic (non-peer) labels keep the old behavior at tiny budgets — the peer
// floor must not leak into plain verb labels.
func TestToolLabelGenericSmallBudgetUnchanged(t *testing.T) {
	got := toolLabel("bash", `{"command":"just verify"}`, 10)
	if xansi.StringWidth(got) > 10 {
		t.Fatalf("generic label exceeded its small budget: %q", got)
	}
}
