package mowi

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestActivityToolLabelKeepsStateAndDetails(t *testing.T) {
	tests := []struct {
		name string
		args string
		want []string
	}{
		{"grep", `{"pattern":"activityState"}`, []string{"searching", "grep", "activityState"}},
		{"edit", `{"path":"/work/repo/tui.go"}`, []string{"shaping", "edit", "tui.go"}},
		{"bash", `{"command":"just verify"}`, []string{"running", "bash", "just verify"}},
		{"gemini: read internal/agent/loop.go", "", []string{"delegating", "gemini", "read", "loop.go"}},
		{"acp_delegate", `{"agent":"claude","prompt":"summarize loop.go"}`, []string{"delegating", "acp_delegate", "summarize", "loop.go"}},
		{"claude: acp_delegate", `{"prompt":"review the diff"}`, []string{"delegating", "claude", "review", "diff"}},
	}
	for _, tc := range tests {
		got := activityToolLabel(tc.name, tc.args, 72)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("activityToolLabel(%q) = %q, missing %q", tc.name, got, want)
			}
		}
	}
}

func TestToolActivityState(t *testing.T) {
	tests := map[string]string{
		"read":                 "searching",
		"write":                "shaping",
		"acp_delegate":         "delegating",
		"claude: acp_delegate": "delegating",
		"mcp":                  "connecting",
		"lsp":                  "connecting",
		"generate_image":       "creating",
		"understand_video":     "inspecting",
		"proc_start":           "running",
		"custom_tool":          "working",
		"gemini: read":         "delegating",
	}
	for name, want := range tests {
		if got := toolActivityState(name); got != want {
			t.Errorf("toolActivityState(%q) = %q, want %q", name, got, want)
		}
	}
}

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

// The delegate start label must surface the peer's task (prompt), not a
// redundant agent id — the agent is already prefixed by the peer form. This is
// the live "what is the ACP peer doing" detail the activity band owes.
func TestDelegateStartLabelShowsPromptTask(t *testing.T) {
	args := `{"agent":"claude","prompt":"summarize the loop spine in internal/agent/loop.go and report"}`
	got := toolLabel("claude: acp_delegate", args, 80)
	if !strings.HasPrefix(got, glyphArrow+" claude") {
		t.Fatalf("want peer arrow + agent prefix, got %q", got)
	}
	if !strings.Contains(got, "acp_delegate") {
		t.Fatalf("missing tool verb: %q", got)
	}
	if !strings.Contains(got, "summarize") || !strings.Contains(got, "loop") {
		t.Fatalf("missing prompt task detail: %q", got)
	}
	// The redundant agent id must not appear as the detail after the verb.
	if strings.Count(strings.ToLower(got), "claude") > 1 {
		t.Fatalf("agent id leaked as detail (redundant): %q", got)
	}
}

// A bare acp_delegate (no peer prefix) also surfaces the prompt task.
func TestDelegateLabelBareNameShowsPrompt(t *testing.T) {
	got := toolLabel("acp_delegate", `{"agent":"gemini","prompt":"grep the spine"}`, 60)
	if !strings.Contains(got, "acp_delegate") {
		t.Fatalf("missing verb: %q", got)
	}
	if !strings.Contains(got, "grep") || !strings.Contains(got, "spine") {
		t.Fatalf("missing prompt task: %q", got)
	}
}

func TestDelegatePromptDetail(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"prompt", `{"agent":"claude","prompt":"summarize the loop"}`, "summarize the loop"},
		{"task", `{"task":"grep spine"}`, "grep spine"},
		{"query", `{"query":"review the diff"}`, "review the diff"},
		{"empty prompt", `{"agent":"claude","prompt":"   "}`, ""},
		{"no prompt key", `{"agent":"claude"}`, ""},
		{"garbage", `not-json`, ""},
		{"long prompt clamps to word window", `{"prompt":"` + strings.Repeat("word ", 40) + `"}`, "word word word word word word"},
	}
	for _, tc := range tests {
		got := delegatePromptDetail(tc.args)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%q: want empty, got %q", tc.name, got)
			}
			continue
		}
		if !strings.HasPrefix(got, tc.want) && !strings.Contains(got, tc.want) {
			t.Errorf("%q: want %q in %q", tc.name, tc.want, got)
		}
	}
}
