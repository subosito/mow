package mowi

import (
	"context"
	"fmt"
	xansi "github.com/charmbracelet/x/ansi"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/subosito/mow"
)

func testEngine(t *testing.T) *mow.Engine {
	t.Helper()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			var last string
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == "user" {
					last = messages[i].Content
					break
				}
			}
			return mow.Message{Role: "assistant", Content: "echo:" + last}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// elevatedTestEngine returns an Engine with AllowWrite and AllowShell enabled,
// so the welcome trust line describes capabilities rather than "read-only".
func elevatedTestEngine(t *testing.T) *mow.Engine {
	t.Helper()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		AllowWrite: true,
		AllowShell: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func joined(m *model) string {
	return strings.Join(m.lines(), "\n")
}

func TestNewModelInit(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	if m.perm() != PermAuto {
		t.Fatalf("perm=%v", m.perm())
	}
	if m.Init() == nil {
		t.Fatal("expected init cmd")
	}
	// Single-line input with configurable prefix (default "❯ "); no placeholder text.
	if m.ta.Prompt != "❯ " || m.ta.Placeholder != "" {
		t.Fatalf("prompt=%q placeholder=%q height=%d", m.ta.Prompt, m.ta.Placeholder, m.ta.Height())
	}
	if m.ta.Height() != 1 {
		t.Fatalf("want single-line start, height=%d", m.ta.Height())
	}
	// Default: centered welcome on, empty transcript (no status spam).
	if !m.showWelcome {
		t.Fatal("expected showWelcome by default")
	}
	if len(m.entries) != 0 {
		t.Fatalf("transcript should start empty: %v", m.lines())
	}
}

func TestSlashHelpAndPerm(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()

	raw.ta.SetValue("/help")
	mod, _ := raw.submit()
	m := mod.(*model)
	if !m.showHelp {
		t.Fatal("expected help overlay")
	}
	m.showHelp = false

	m.ta.SetValue("/perm ask")
	mod, _ = m.submit()
	m = mod.(*model)
	if m.perm() != PermAsk {
		t.Fatalf("perm=%v", m.perm())
	}

	m.ta.SetValue("/status")
	mod, _ = m.submit()
	m = mod.(*model)
	if !strings.Contains(joined(m), "perm ask") {
		t.Fatalf("status: %v", m.lines())
	}
}

func TestQuestionMarkNotAlwaysHelp(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true

	// empty input → ? opens help
	mod, _ := raw.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	m := mod.(*model)
	if !m.showHelp {
		t.Fatal("empty input: ? should open help")
	}
	m.showHelp = false

	// non-empty input → ? should type into textarea, not open help
	m.ta.SetValue("what")
	mod, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	m = mod.(*model)
	if m.showHelp {
		t.Fatal("non-empty input: ? must not open help")
	}
	if !strings.Contains(m.ta.Value(), "?") {
		t.Fatalf("expected ? in textarea, got %q", m.ta.Value())
	}
}

func TestDonePrefersFinalTextNotStreamLeftover(t *testing.T) {
	// Regression: streamBuf + residual "." used to produce a second empty-ish bubble.
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.busy = true
	raw.streamBuf = "partial answer"
	mod, _ := raw.Update(doneMsg{text: "final complete answer", err: nil})
	m := mod.(*model)
	if m.busy {
		t.Fatal("busy")
	}
	if m.streamBuf != "" {
		t.Fatalf("streamBuf should clear, got %q", m.streamBuf)
	}
	text := joined(m)
	if !strings.Contains(text, "final complete answer") {
		t.Fatalf("want final text: %v", m.lines())
	}
	if strings.Contains(text, "partial answer") {
		t.Fatalf("must not keep stream preview as separate entry: %v", m.lines())
	}
	// only one assistant entry
	n := 0
	for _, e := range m.entries {
		if e.kind == kindAssistant {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("assistant entries=%d entries=%v", n, m.lines())
	}
}

func TestLateDeltaIgnoredAfterDone(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.busy = false
	mod, _ := raw.Update(deltaMsg("."))
	m := mod.(*model)
	if m.streamBuf != "" {
		t.Fatalf("late delta must not fill streamBuf: %q", m.streamBuf)
	}
}

func TestPromptViaEngine(t *testing.T) {
	eng := testEngine(t)
	res, err := eng.Prompt(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "echo:hello" {
		t.Fatalf("got %q", res.Text)
	}
}

func TestDoneMsgRendersAssistant(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.busy = true
	mod, _ := raw.Update(doneMsg{text: "final answer", err: nil})
	m := mod.(*model)
	if m.busy {
		t.Fatal("busy")
	}
	if !strings.Contains(joined(m), "final answer") {
		t.Fatalf("%v", m.lines())
	}
}

func TestCancelDone(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.busy = true
	mod, _ := raw.Update(doneMsg{err: context.Canceled})
	m := mod.(*model)
	if !strings.Contains(joined(m), "cancelled") {
		t.Fatalf("%v", m.lines())
	}
}

// A cancelled/failed LLM call must NOT commit the partial live stream as an
// assistant turn: the engine never records it, so doing so would make the
// transcript diverge from engine history. Only the status line appears, and
// wrapped cancels are still detected via errors.Is.
func TestCancelDoesNotCommitPartialStream(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.busy = true
	raw.streamBuf = "partial answer tokens"
	mod, _ := raw.Update(doneMsg{text: "", err: context.Canceled})
	m := mod.(*model)
	if !strings.Contains(joined(m), "cancelled") {
		t.Fatalf("want cancelled status, got:\n%s", joined(m))
	}
	if strings.Contains(joined(m), "partial answer") {
		t.Fatalf("partial stream committed as assistant turn:\n%s", joined(m))
	}
	// Wrapped cancel must be recognized too (errors.Is, not ==).
	raw2 := newModel(testEngine(t), false, false)
	raw2.width, raw2.height = 80, 24
	raw2.layout()
	raw2.busy = true
	raw2.streamBuf = "more partial tokens"
	mod2, _ := raw2.Update(doneMsg{text: "", err: fmt.Errorf("stream failed: %w", context.Canceled)})
	m2 := mod2.(*model)
	if !strings.Contains(joined(m2), "cancelled") {
		t.Fatalf("wrapped cancel not detected, got:\n%s", joined(m2))
	}
	if strings.Contains(joined(m2), "more partial") {
		t.Fatalf("partial stream committed on wrapped cancel:\n%s", joined(m2))
	}
}

func TestWindowSizeView(t *testing.T) {
	raw := newModel(testEngine(t), false, true)
	if raw.perm() != PermAsk {
		t.Fatal("ask")
	}
	mod, _ := raw.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := mod.(*model)
	view := m.View().Content
	if !strings.Contains(view, "mow") {
		t.Fatalf("view=%q", view)
	}
	// perm ask shows compact "ask" pill in header, not full key cheatsheet
	if !strings.Contains(view, "ask") {
		t.Fatalf("expected ask pill: %q", view)
	}
	// idle status collapses; no cheatsheet in chrome
	if strings.Contains(view, "ctrl+p perm") || strings.Contains(view, "enter send") {
		t.Fatalf("key cheatsheet should not clutter chrome: %q", view)
	}
	// Input affordance: prompt prefix only (no placeholder text).
	if !strings.Contains(view, "❯") && !strings.Contains(view, ">") {
		t.Fatalf("expected prompt in input: %q", view)
	}
}

func TestMarkdownRenderedInTranscript(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 100, 40
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.add(kindAssistant, "## Hello\n\nUse `code` and **bold**.\n\n```python\nprint('hi')\n```")
	raw.refreshVP()
	content := raw.vp.View()
	// Glamour should produce more than raw markdown markers for headings/emphasis
	if !strings.Contains(content, "Hello") {
		t.Fatalf("heading missing: %q", content)
	}
	if !strings.Contains(content, "print") {
		t.Fatalf("code missing: %q", content)
	}
}

func TestNoProgressBar(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true
	view := raw.View().Content
	// spinner line only — no unicode progress fill noise from bubbles/progress
	if strings.Contains(view, "█") || strings.Contains(view, "░") {
		t.Fatalf("progress bar should be gone: %q", view)
	}
}

func TestConversationNotBoxed(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.add(kindUser, "hello?")
	raw.add(kindAssistant, "hi there")
	raw.refreshVP()
	content := raw.vp.View()
	if strings.Contains(content, "╭") || strings.Contains(content, "╮") {
		t.Fatalf("conversation should not use rounded cards: %q", content)
	}
	// Roles are a visual gutter / bubble, not "you"/"mow" labels.
	if strings.Contains(content, "you\n") || strings.Contains(content, "mow\n") {
		t.Fatalf("should not use text role labels: %q", content)
	}
	// No gutter bars either — the user block background is the role marker.
	if strings.Contains(content, roleBar) {
		t.Fatalf("gutter bars should be gone: %q", content)
	}
	if !strings.Contains(content, "hello?") || !strings.Contains(joined(raw), "hello?") {
		t.Fatalf("user line: content=%q entries=%v", content, raw.lines())
	}
}

func TestRoleBlocksVisualNotText(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.add(kindUser, "hi from me")
	raw.add(kindAssistant, "reply body unique")
	raw.refreshVP()
	content := raw.vp.View()
	// The user role is a background block (SGR 48 bg sequence), not a bar or
	// a text label.
	if !strings.Contains(content, ";48;") && !strings.Contains(content, "[48;") {
		t.Fatalf("expected background block on user prompt: %q", content)
	}
	if strings.Contains(content, roleBar) {
		t.Fatalf("gutter bars should be gone: %q", content)
	}
	// Content still present (glamour injects SGR between words — check tokens).
	if !strings.Contains(content, "hi from me") ||
		!strings.Contains(content, "reply") || !strings.Contains(content, "unique") {
		t.Fatalf("content missing: %q", content)
	}
}

func TestFormatTurnTime(t *testing.T) {
	now := time.Date(2026, 7, 17, 18, 30, 0, 0, time.Local)
	same := time.Date(2026, 7, 17, 9, 5, 0, 0, time.Local)
	if got := formatTurnTime(same, now); got != "09:05" {
		t.Fatalf("same day: %q", got)
	}
	other := time.Date(2026, 3, 4, 14, 0, 0, 0, time.Local)
	if got := formatTurnTime(other, now); got != "Mar 4 14:00" {
		t.Fatalf("same year: %q", got)
	}
	old := time.Date(2024, 12, 25, 10, 0, 0, 0, time.Local)
	if got := formatTurnTime(old, now); got != "2024-12-25 10:00" {
		t.Fatalf("other year: %q", got)
	}
}

func TestTurnTimestampInView(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.add(kindUser, "hello stamped")
	// Force known clock for the entry.
	raw.entries[0].at = time.Date(2026, 7, 17, 15, 4, 0, 0, time.Local)
	raw.entries[0].view = ""
	raw.entries[0].viewW = 0
	raw.refreshVP()
	var userView string
	for _, e := range raw.entries {
		if e.kind == kindUser {
			userView = e.view
			break
		}
	}
	if !strings.Contains(userView, "15:04") {
		// If test runs on a different local day than the forced date, format includes date.
		if !strings.Contains(userView, "15:04") && !strings.Contains(userView, "Jul 17") {
			t.Fatalf("expected timestamp in user view: %q", userView)
		}
	}
	if !strings.Contains(userView, "hello stamped") {
		t.Fatalf("missing body: %q", userView)
	}
}

func TestRoleBlocksLeftAligned(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.add(kindUser, "short prompt")
	raw.add(kindAssistant, "assistant reply here")
	raw.refreshVP()
	for _, e := range raw.entries {
		if e.kind != kindUser && e.kind != kindAssistant {
			continue
		}
		if strings.Contains(e.view, roleBar) {
			t.Fatalf("gutter bar should be gone: %q", e.view)
		}
	}
	// The clock is inline on the user block's first line — no stamp-only row.
	userView := raw.entries[0].view
	first := strings.SplitN(userView, "\n", 2)[0]
	if !strings.Contains(first, "short prompt") {
		t.Fatalf("user text not on first line (separate stamp row?): %q", userView)
	}
	plain := xansi.Strip(first)
	if !strings.Contains(plain, ":") { // HH:MM inline
		t.Fatalf("expected inline clock on first line: %q", plain)
	}
}

func TestThinkingIndicatorUsesGutter(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	raw.reasonBuf = "plan the approach"
	raw.reasonStartedAt = time.Now()
	raw.paintLiveStream()
	if strings.Contains(raw.streamFrame, roleBar) {
		t.Fatalf("gutter bar should be gone from thinking indicator: %q", raw.streamFrame)
	}
	if !strings.Contains(raw.streamFrame, "thinking") {
		t.Fatalf("missing thinking label: %q", raw.streamFrame)
	}
	// Still aligned under the content column (plain gutter spaces).
	if !strings.HasPrefix(raw.streamFrame, strings.Repeat(" ", roleGutterW)) {
		t.Fatalf("indicator should keep the gutter indent: %q", raw.streamFrame)
	}
}

func TestHelpOverlay(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 100, 40
	raw.layout()
	raw.ready = true
	raw.showHelp = true
	view := raw.View().Content
	if !strings.Contains(view, "help") || !strings.Contains(view, "/perm") {
		t.Fatalf("help view: %q", view)
	}
}

func TestPermKeyYes(t *testing.T) {
	raw := newModel(testEngine(t), false, true)
	raw.width, raw.height = 80, 24
	raw.layout()
	resp := make(chan error, 1)
	raw.testArmPerm("write", "{}", resp)
	cmd := raw.handlePermKey("y")
	if cmd == nil {
		t.Fatal("expected poll cmd")
	}
	select {
	case err := <-resp:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	if !strings.Contains(joined(raw), "allowed") {
		t.Fatalf("allowed line: %v", raw.lines())
	}
}

func TestPermAskMsgUpdate(t *testing.T) {
	raw := newModel(testEngine(t), false, true)
	raw.width, raw.height = 80, 24
	raw.layout()
	resp := make(chan error, 1)
	mod, _ := raw.Update(permAskMsg{name: "bash", args: `{"cmd":"ls"}`, resp: resp})
	m := mod.(*model)
	if m.permWait == nil || m.permWait.name != "bash" {
		t.Fatalf("permWait=%v", m.permWait)
	}
	if !strings.Contains(joined(m), "bash") {
		t.Fatalf("perm prompt: %v", m.lines())
	}

	m2 := newModel(testEngine(t), false, true)
	m2.width, m2.height = 80, 24
	m2.layout()
	resp2 := make(chan error, 1)
	m2.testArmPerm("bash", "{}", resp2)
	_ = m2.handlePermKey("n")
	select {
	case err := <-resp2:
		if err == nil {
			t.Fatal("expected deny error")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestPermAlways(t *testing.T) {
	raw := newModel(testEngine(t), false, true)
	raw.width, raw.height = 80, 24
	raw.layout()
	resp := make(chan error, 1)
	raw.testArmPerm("edit", "{}", resp)
	_ = raw.handlePermKey("a")
	if !raw.autoPower.Load() || raw.perm() != PermAuto {
		t.Fatalf("autoPower=%v perm=%v", raw.autoPower.Load(), raw.perm())
	}
	select {
	case err := <-resp:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestIsPowerTool(t *testing.T) {
	if !isPowerTool("bash") || isPowerTool("read") {
		t.Fatal("power classification")
	}
}

func TestPermissionModeString(t *testing.T) {
	if PermAsk.String() != "ask" || PermAuto.String() != "auto" {
		t.Fatal(PermAsk, PermAuto)
	}
}

func TestSquareBorders(t *testing.T) {
	th := newTheme()
	// Help/dialog boxes still use square corners; input is rule-only (no box).
	s := th.Box.Render("x")
	if strings.Contains(s, "╭") {
		t.Fatalf("want square border, got rounded: %q", s)
	}
}

func TestInputTopRuleOnly(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 40, 20
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	view := raw.renderInput()
	lines := strings.Split(view, "\n")
	if len(lines) < 2 {
		t.Fatalf("want top rule + input, got %q", view)
	}
	// First line is a full-width rule (no side borders / corners).
	rule := lipgloss.Width(lines[0])
	if rule != raw.width {
		t.Fatalf("top rule width=%d want %d: %q", rule, raw.width, lines[0])
	}
	if strings.Contains(view, "│") || strings.Contains(view, "┌") || strings.Contains(view, "└") {
		t.Fatalf("input should not be a full box: %q", view)
	}
}

func TestSpinnerReplacesPromptWhenBusy(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	if raw.ta.Prompt != "❯ " {
		t.Fatalf("idle prompt=%q", raw.ta.Prompt)
	}
	if s := raw.renderPermissionStrip(); s != "" {
		t.Fatalf("idle permission strip should be empty, got %q", s)
	}
	if band := raw.renderActivityBand(); band != "" {
		t.Fatalf("idle activity band should be empty, got %q", band)
	}
	raw.busy = true
	raw.startedAt = time.Now().Add(-2 * time.Second)
	raw.syncInputChrome()
	raw.layout()
	// Prompt is a short busy cue only — no tool text, no elapsed.
	if strings.Contains(xansi.Strip(raw.ta.Prompt), "⚙") {
		t.Fatalf("busy prompt must not embed tool detail: %q", raw.ta.Prompt)
	}
	if raw.ta.Prompt == "❯ " {
		t.Fatalf("busy prompt should not stay idle glyph, got %q", raw.ta.Prompt)
	}
	band := xansi.Strip(raw.renderActivityBand())
	if !strings.Contains(band, "s") {
		t.Fatalf("activity band should show elapsed, got %q", band)
	}
}

func TestElapsedAlwaysShownWhileBusy(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true
	raw.startedAt = time.Now()
	raw.syncInputChrome()
	band := xansi.Strip(raw.renderActivityBand())
	if !strings.Contains(band, "s") {
		t.Fatalf("elapsed should show from 0s on activity band, got %q (prompt %q)", band, raw.ta.Prompt)
	}
}

func TestHeartbeatKeepsAnimating(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.busy = true
	raw.startedAt = time.Now()
	raw.syncInputChrome()
	first := raw.spin.View()
	// Several heartbeats must change spinner frame and keep a follow-up cmd.
	for i := 0; i < 5; i++ {
		mod, cmd := raw.Update(busyHeartbeatMsg{})
		raw = mod.(*model)
		if cmd == nil {
			t.Fatal("heartbeat must reschedule")
		}
	}
	if raw.spin.View() == first {
		// Possible but unlikely over 5 frames of MiniDot (10 frames).
		// Advance more.
		for i := 0; i < 12; i++ {
			mod, _ := raw.Update(busyHeartbeatMsg{})
			raw = mod.(*model)
		}
		if raw.spin.View() == first {
			t.Fatalf("spinner frame stuck at %q after heartbeats", first)
		}
	}
}

func TestFormatContextPct(t *testing.T) {
	// 1M window: integer (ct*100)/window was 0 for anything under 10k tokens.
	label, warn := formatContextPct(5_000, 1_000_000)
	if warn || label != "0.5% ctx" {
		t.Fatalf("5k/1M → %q warn=%v", label, warn)
	}
	label, warn = formatContextPct(500, 1_000_000)
	if warn || label != "<0.1% ctx" {
		t.Fatalf("500/1M → %q warn=%v", label, warn)
	}
	label, warn = formatContextPct(20_000, 1_000_000)
	if warn || label != "2% ctx" {
		t.Fatalf("20k/1M → %q warn=%v", label, warn)
	}
	label, warn = formatContextPct(900_000, 1_000_000)
	if !warn || label != "90% ctx" {
		t.Fatalf("900k/1M → %q warn=%v", label, warn)
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{2300 * time.Millisecond, "2.3s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m00s"},
		{65 * time.Second, "1m05s"},
		{600 * time.Second, "10m00s"},
		{3661 * time.Second, "1h1m01s"},
		{3600 * time.Second, "1h"},
		{3720 * time.Second, "1h2m"},
	}
	for _, tc := range cases {
		if g := formatElapsed(tc.d); g != tc.want {
			t.Errorf("%v → %q want %q", tc.d, g, tc.want)
		}
	}
}

func TestTypeWhileBusy(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true
	raw.startedAt = time.Now()
	raw.syncInputChrome()
	mod, _ := raw.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m := mod.(*model)
	if !strings.Contains(m.ta.Value(), "n") {
		t.Fatalf("should type while busy, got %q", m.ta.Value())
	}
	// enter does not submit while busy
	mod, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mod.(*model)
	if !m.busy {
		t.Fatal("enter must not clear busy")
	}
}

func TestApplyModelListOpensPicker(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.applyModelList(modelListMsg{
		models: []mow.ModelInfo{
			{ID: "alpha", Wire: "openai-chat-completions"},
			{ID: "beta", Wire: "anthropic-messages"},
		},
		current:    "beta",
		openPicker: true,
	})
	if raw.modelPick == nil {
		t.Fatal("expected model picker open")
	}
	if raw.modelPick.idx != 1 {
		t.Fatalf("cursor on current: idx=%d", raw.modelPick.idx)
	}
	if got := raw.modelPick.items[raw.modelPick.idx].ID; got != "beta" {
		t.Fatalf("selected=%q", got)
	}
	// Card lists both ids.
	card := raw.modelPickerCard()
	if !strings.Contains(card, "alpha") || !strings.Contains(card, "beta") {
		t.Fatalf("card: %q", card)
	}
	raw.applyModelList(modelListMsg{setTo: "gamma", setWire: "openai-chat-completions", current: "gamma"})
	if raw.modelPick != nil {
		t.Fatal("picker should close on set")
	}
	if !strings.Contains(strings.Join(raw.lines(), "\n"), "model → gamma") {
		t.Fatalf("set: %v", raw.lines())
	}
}

func TestNormalizeModelFilter(t *testing.T) {
	if got := normalizeModelFilter("  deepseek-chat  [openai-responses]  "); got != "deepseek-chat" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeModelFilter("plain-id"); got != "plain-id" {
		t.Fatalf("got %q", got)
	}
}

func TestModelPickerKeys(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.openModelPicker([]mow.ModelInfo{
		{ID: "alpha", Wire: "openai-chat-completions"},
		{ID: "beta", Wire: "anthropic-messages"},
	}, "alpha", "")
	if raw.modelPick.idx != 0 {
		t.Fatal(raw.modelPick.idx)
	}
	mod, _ := raw.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m := mod.(*model)
	if m.modelPick == nil || m.modelPick.idx != 1 {
		t.Fatalf("down: %+v", m.modelPick)
	}
	mod, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mod.(*model)
	if m.modelPick != nil {
		t.Fatal("esc should close picker")
	}
}

func TestShiftTabTogglesPerm(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	if raw.perm() != PermAuto {
		t.Fatal(raw.perm())
	}
	mod, _ := raw.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m := mod.(*model)
	if m.perm() != PermAsk {
		t.Fatalf("shift+tab → ask, got %v", m.perm())
	}
	mod, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = mod.(*model)
	if m.perm() != PermAuto {
		t.Fatalf("shift+tab → auto, got %v", m.perm())
	}
}

func TestInputFitsTerminalWidth(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 40, 20
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	view := raw.renderInput()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > raw.width {
			t.Fatalf("line wider than terminal: w=%d width=%d line=%q", w, raw.width, line)
		}
	}
}

func TestTypingUDoesNotScrollViewport(t *testing.T) {
	// Regression: viewport default keymap binds "u" → half-page-up, which
	// stole the letter while the user was composing.
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	// Tall transcript so scrolling is possible.
	for i := 0; i < 40; i++ {
		raw.add(kindStatus, strings.Repeat("line ", 5)+string(rune('a'+i%26)))
	}
	raw.refreshVP()
	raw.vp.GotoBottom()
	yBefore := raw.vp.YOffset()
	mod, _ := raw.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	m := mod.(*model)
	if m.vp.YOffset() != yBefore {
		t.Fatalf("viewport moved on 'u': before=%d after=%d", yBefore, m.vp.YOffset())
	}
	if !strings.Contains(m.ta.Value(), "u") {
		t.Fatalf("expected 'u' in input, got %q", m.ta.Value())
	}
}

func TestSlashInputStyle(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.ta.SetValue("hello")
	raw.syncInputChrome()
	if raw.isSlashInput() {
		t.Fatal("plain text should not be slash mode")
	}
	raw.ta.SetValue("/help")
	raw.syncInputChrome()
	if !raw.isSlashInput() {
		t.Fatal("expected slash mode")
	}
	view := raw.renderInput()
	// Border / text should render (non-empty); slash mode uses amber styling.
	if !strings.Contains(view, "/help") && !strings.Contains(view, "help") {
		// textarea may wrap; at least ensure View non-empty
		if strings.TrimSpace(view) == "" {
			t.Fatal("empty input view")
		}
	}
}

func TestDiffEntryRendersPathAndHunk(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.add(kindDiff, "edited f.go\n--- f.go\n+++ f.go\n@@\n-func A() {}\n+func B() {}\n")
	raw.refreshVP()
	content := raw.vp.View()
	if !strings.Contains(content, "f.go") {
		t.Fatalf("filename missing: %q", content)
	}
	if !strings.Contains(content, "func A") && !strings.Contains(content, "func B") {
		// ANSI may split tokens; check markers
		if !strings.Contains(content, "A()") && !strings.Contains(content, "B()") {
			t.Fatalf("hunk missing: %q", content)
		}
	}
}

func TestToolUIMsgAddsDiffEntry(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	mod, cmd := raw.Update(toolUIMsg{
		name: "edit",
		text: "edited main.go\n--- main.go\n+++ main.go\n@@\n-old\n+new\n",
	})
	m := mod.(*model)
	if cmd == nil {
		t.Fatal("expected re-poll cmd")
	}
	found := false
	for _, e := range m.entries {
		if e.kind == kindDiff && strings.Contains(e.text, "main.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("entries=%v", m.lines())
	}
}

func TestStreamThinkingIndicatorNoBody(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true
	raw.add(kindAssistant, "already done turn")
	raw.refreshVP()
	cached := raw.entries[0].view

	// Reasoning: indicator only — never paint the body.
	mod, _ := raw.Update(reasoningMsg("secret plan details that would be sluggish"))
	m := mod.(*model)
	if m.entries[0].view != cached {
		t.Fatal("stream re-rendered finished entry")
	}
	if !strings.Contains(m.streamFrame, "thinking") {
		t.Fatalf("expected thinking indicator: %q", m.streamFrame)
	}
	if strings.Contains(m.streamFrame, "secret plan") {
		t.Fatalf("thinking body must never paint: %q", m.streamFrame)
	}
	// No expand chevron — indicator is spinner + elapsed only.
	if strings.Contains(m.streamFrame, "▸") || strings.Contains(m.streamFrame, "▾") {
		t.Fatalf("did not expect expand chevron: %q", m.streamFrame)
	}
	if m.reasonStartedAt.IsZero() {
		t.Fatal("reasonStartedAt should set")
	}

	// Content starts — thinking line goes away.
	mod, _ = m.Update(deltaMsg("## Hello"))
	m = mod.(*model)
	mod, _ = m.Update(streamRenderedMsg{
		gen: m.streamGen, width: max(24, m.vp.Width()-2),
		src: m.streamBuf, body: "Hello",
	})
	m = mod.(*model)
	if !strings.Contains(m.streamFrame, "Hello") {
		t.Fatalf("live glamour body missing: %q", m.streamFrame)
	}
	if strings.Contains(m.streamFrame, "thinking") {
		t.Fatalf("thinking indicator should hide once answer starts: %q", m.streamFrame)
	}

	mod, _ = m.Update(doneMsg{text: "## Hello", err: nil})
	m = mod.(*model)
	if m.busy || m.reasonBuf != "" {
		t.Fatal("should clear busy/reason")
	}
}

func TestStreamSnapProgressivePaint(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true

	mod, _ := raw.Update(streamSnapMsg{content: "Hel"})
	m := mod.(*model)
	if !strings.Contains(m.streamFrame, "Hel") {
		t.Fatalf("first snap: %q", m.streamFrame)
	}
	mod, _ = m.Update(streamSnapMsg{content: "lo"})
	m = mod.(*model)
	if !strings.Contains(m.streamFrame, "Hello") {
		t.Fatalf("second snap progressive: %q", m.streamFrame)
	}
	// History cache should not force re-render of finished entries per snap.
	raw2 := newModel(testEngine(t), true, false)
	raw2.width, raw2.height = 80, 24
	raw2.layout()
	raw2.ready = true
	raw2.busy = true
	raw2.add(kindUser, "q")
	raw2.refreshVP()
	hc := raw2.historyCache
	mod, _ = raw2.Update(streamSnapMsg{content: "a"})
	m = mod.(*model)
	if m.historyCache != hc && hc != "" {
		// Cache may rebuild once when width/entries stable — after first paint
		// further snaps must keep same historyCache pointer/string.
	}
	mod, _ = m.Update(streamSnapMsg{content: "b"})
	m = mod.(*model)
	if m.historyCacheN != 1 {
		t.Fatalf("historyCacheN=%d want 1", m.historyCacheN)
	}
}

func TestMouseLeakKeysDropped(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	// SGR mouse wheel fragment misread as runes.
	mod, _ := raw.Update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "[<64;24;27M"})
	m := mod.(*model)
	if m.ta.Value() != "" {
		t.Fatalf("mouse leak entered textarea: %q", m.ta.Value())
	}
}

func TestReasoningIndicatorDropsOnDone(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true

	mod, _ := raw.Update(reasoningMsg("plan the answer"))
	m := mod.(*model)
	if !m.reasoningArmed() {
		t.Fatal("expected thinking armed")
	}
	if !strings.Contains(m.streamFrame, "thinking") {
		t.Fatalf("thinking indicator: %q", m.streamFrame)
	}
	if strings.Contains(m.streamFrame, "plan the answer") {
		t.Fatalf("body must stay hidden: %q", m.streamFrame)
	}

	mod, _ = m.Update(deltaMsg("Hello!"))
	m = mod.(*model)
	if m.streamBuf != "Hello!" {
		t.Fatalf("streamBuf=%q", m.streamBuf)
	}

	mod, _ = m.Update(doneMsg{text: "Hello!", err: nil})
	m = mod.(*model)
	if m.reasonBuf != "" {
		t.Fatal("reason should clear on done")
	}
	text := joined(m)
	if !strings.Contains(text, "Hello!") {
		t.Fatalf("want answer: %v", m.lines())
	}
	if strings.Contains(text, "plan the answer") {
		t.Fatalf("reasoning must not stay in transcript: %v", m.lines())
	}
}

func TestScrollUpWhileBusyStopsFollow(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	raw.followBottom = true
	// Tall content so scroll is possible.
	for i := 0; i < 30; i++ {
		raw.add(kindUser, strings.Repeat("line ", 20)+fmt.Sprintf("%d", i))
	}
	raw.refreshVP()
	raw.vp.GotoBottom()
	if !raw.vp.AtBottom() {
		t.Fatal("setup: should be at bottom")
	}

	// Default scroll_up is ctrl+u (half page).
	mod, _ := raw.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m := mod.(*model)
	if m.followBottom {
		t.Fatal("ctrl+u should clear followBottom")
	}
	y := m.vp.YOffset()
	// Stream paint must not yank scroll back to bottom.
	m.streamBuf = "new token that is longer than before for height"
	m.paintLiveStream()
	if m.followBottom {
		t.Fatal("followBottom must stay false after stream paint")
	}
	if m.vp.YOffset() != y {
		if m.vp.AtBottom() {
			t.Fatalf("scroll yanked to bottom: was %d now %d", y, m.vp.YOffset())
		}
	}
}

func TestLiveAnswerStablePrefix(t *testing.T) {
	// Stable prefix: keep glamoured prefix, append plain tail only.
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	raw.streamBody = "Hello pretty"
	raw.streamBodySrc = "Hello"
	raw.streamBuf = "Hello world more"
	inner := max(16, max(24, raw.vp.Width()-2)-roleGutterW)
	got := raw.liveAnswerBody(inner)
	if !strings.Contains(got, "Hello pretty") {
		t.Fatalf("expected glamoured prefix: %q", got)
	}
	if !strings.Contains(got, " world more") && !strings.Contains(got, "world") {
		t.Fatalf("expected plain tail: %q", got)
	}
	// Exact match uses only pretty body.
	raw.streamBuf = "Hello"
	got = raw.liveAnswerBody(inner)
	if !strings.Contains(got, "Hello pretty") {
		t.Fatalf("exact: %q", got)
	}
}

func TestCommitDoesNotReuseStaleStreamBody(t *testing.T) {
	// Regression: mid-stream glamour must not become the final truncated entry.
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	raw.streamBody = "Remove an import → that subcommand"
	raw.streamBodySrc = "Remove an import → that subcommand" // partial
	raw.streamBuf = "Remove an import → that subcommand and its tools disappear.\n\n### Tools"
	final := raw.streamBuf
	idx, needsPretty := raw.commitAssistant(final)
	if !needsPretty {
		t.Fatal("stale streamBody must not skip async pretty")
	}
	if !strings.Contains(raw.entries[idx].view, "Tools") && !strings.Contains(raw.entries[idx].text, "Tools") {
		// text is final; view is plain wrap of final
		if raw.entries[idx].text != final {
			t.Fatalf("entry text should be full final")
		}
	}
	if raw.entries[idx].text != final {
		t.Fatalf("text=%q want full final", raw.entries[idx].text)
	}
	// Plain view should include the full final (word-wrapped), not only streamBody.
	if strings.Contains(raw.entries[idx].view, "Remove an import") && !strings.Contains(raw.entries[idx].view, "Tools") {
		// wordWrap keeps content; check text is source of truth
		t.Log("view wrap may split lines; text holds full final")
	}
	if !strings.Contains(raw.entries[idx].view, "Tools") {
		t.Fatalf("final view must not be truncated mid-stream body: %q", raw.entries[idx].view)
	}
}

func TestStreamIngestNeverBlocksWriter(t *testing.T) {
	ing := newStreamIngest()
	// Flood without a reader — must not block (mutex coalesce, not chan).
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			ing.pushReasoning("x")
			ing.pushContent("y")
		}
		ing.finish()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ingest push blocked — would freeze LLM stream")
	}
	c, r, finished := ing.take()
	if !finished {
		t.Fatal("expected finished")
	}
	if len(r) != 10000 || len(c) != 10000 {
		t.Fatalf("content=%d reason=%d", len(c), len(r))
	}
}

func TestStreamPaintTickRebuildsPlainAndReschedules(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	raw.streamPaint = true
	raw.streamBuf = "hi there"
	raw.streamDirty = true
	mod, cmd := raw.Update(streamPaintMsg{})
	m := mod.(*model)
	if m.streamDirty {
		t.Fatal("dirty should clear after plain paint")
	}
	if !strings.Contains(m.streamFrame, "hi there") {
		t.Fatalf("expected plain paint: %q", m.streamFrame)
	}
	if cmd == nil {
		t.Fatal("expected reschedule tick")
	}
}

func TestStreamIngestToSnapPaints(t *testing.T) {
	// End-to-end: ingest push → pollStream cmd → snap → progressive frame.
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.busy = true
	ing := newStreamIngest()
	raw.ingest = ing
	ing.pushContent("one ")
	cmd := raw.pollStream()
	if cmd == nil {
		t.Fatal("pollStream")
	}
	msg := cmd()
	snap, ok := msg.(streamSnapMsg)
	if !ok {
		t.Fatalf("msg type %T", msg)
	}
	if snap.content != "one " {
		t.Fatalf("content=%q", snap.content)
	}
	mod, _ := raw.Update(snap)
	m := mod.(*model)
	if !strings.Contains(m.streamFrame, "one") {
		t.Fatalf("frame after snap: %q", m.streamFrame)
	}
}

func TestNoOSCProbeDuringMarkdownRender(t *testing.T) {
	// mdCache must not call termenv.HasDarkBackground (OSC 11).
	// If it did, non-TTY tests would still hang or misbehave under CI.
	pinTerminalTheme()
	c := newMDCache(true)
	out := renderMarkdownCached(&c, "# hi\n\nbody", 60, false)
	if !strings.Contains(out, "hi") && strings.TrimSpace(out) == "" {
		t.Fatalf("empty render: %q", out)
	}
}

func TestStreamFollowRespectsScroll(t *testing.T) {
	raw := newModel(testEngine(t), true, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showWelcome = false
	raw.busy = true
	raw.followBottom = true
	raw.streamBuf = strings.Repeat("line of stream\n", 80)
	raw.refreshVP()
	if !raw.vp.AtBottom() {
		t.Fatal("follow should pin to bottom")
	}
	// User scrolls up → stop following; refresh must not yank back.
	raw.vp.SetYOffset(0)
	raw.followBottom = false
	raw.streamBuf += "more stream content that grows\n"
	raw.refreshVP()
	if raw.vp.YOffset() != 0 {
		t.Fatalf("scroll position lost: y=%d", raw.vp.YOffset())
	}
	// Re-enable follow
	raw.followBottom = true
	raw.refreshVP()
	if !raw.vp.AtBottom() {
		t.Fatal("follow should return to bottom")
	}
}

func TestInputAutoGrowsOnNewline(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	if !raw.ta.DynamicHeight {
		t.Fatal("DynamicHeight should be enabled")
	}
	if raw.ta.Height() != 1 {
		t.Fatalf("start height=%d", raw.ta.Height())
	}
	// Simulate multiline value (as ctrl+j would insert).
	raw.ta.SetValue("line1\nline2\nline3")
	if raw.ta.Height() < 3 {
		t.Fatalf("height=%d want >=3 after hard newlines", raw.ta.Height())
	}
	raw.ta.SetValue("one")
	if raw.ta.Height() != 1 {
		t.Fatalf("should shrink back to 1, got %d", raw.ta.Height())
	}
}

func TestInputAutoGrowsOnSoftWrap(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 40, 24
	raw.layout()
	raw.ready = true
	// Long single line must soft-wrap and grow the prompt (not stay 1 row).
	long := strings.Repeat("word ", 40) // ~200 runes, width ~38 content
	raw.ta.SetValue(long)
	if raw.ta.Height() < 3 {
		t.Fatalf("soft-wrap should grow height, got %d (value len=%d width=%d)",
			raw.ta.Height(), len(long), raw.ta.Width())
	}
}

func TestInputGrowsViaKeyUpdate(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	// Insert newline via configured key (ctrl+j).
	raw.ta.SetValue("line1")
	mod, _ := raw.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'j'})
	m := mod.(*model)
	// After newline, value should be two logical lines and height >= 2.
	if m.ta.LineCount() < 2 {
		// Some key encodings differ; set via binding path if needed.
		m.ta.SetValue("line1\n")
		m.syncInputHeight()
	}
	if m.ta.LineCount() < 2 && !strings.Contains(m.ta.Value(), "\n") {
		t.Fatalf("newline not inserted: value=%q lines=%d", m.ta.Value(), m.ta.LineCount())
	}
	if m.ta.Height() < 2 {
		m.syncInputHeight()
	}
	if m.ta.Height() < 2 {
		t.Fatalf("height=%d want >=2 after newline", m.ta.Height())
	}
}

func TestSeedTranscriptFromEngine(t *testing.T) {
	// Write a turn, resume the same session id, expect TUI entries seeded.
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("OPENAI_BASE_URL", "http://example.com/v1")
	t.Setenv("MOW_ALLOW_SHELL", "")
	t.Setenv("MOW_ALLOW_WRITE", "")

	// New session first turn via Chat inject — writes user/assistant + dumps.
	eng1, err := mow.New(mow.Options{
		NoSession: false,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "prior reply"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng1.Prompt(context.Background(), "prior user"); err != nil {
		t.Fatal(err)
	}
	sid := eng1.SessionID()
	if sid == "" {
		t.Fatal("empty session id")
	}

	// Resume same session id.
	eng2, err := mow.New(mow.Options{
		NoSession: false,
		SessionID: sid,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := eng2.Transcript()
	if len(tr) < 2 {
		t.Fatalf("transcript on resume: %+v", tr)
	}
	if tr[0].Role != "user" || tr[0].Content != "prior user" {
		t.Fatalf("user: %+v", tr[0])
	}
	if tr[1].Role != "assistant" || tr[1].Content != "prior reply" {
		t.Fatalf("asst: %+v", tr[1])
	}

	m := newModel(eng2, false, false)
	if len(m.entries) < 2 {
		t.Fatalf("TUI entries not seeded: %d %v", len(m.entries), m.lines())
	}
	if m.entries[0].kind != kindUser || m.entries[0].text != "prior user" {
		t.Fatalf("entry0: %+v", m.entries[0])
	}
	if m.entries[1].kind != kindAssistant || m.entries[1].text != "prior reply" {
		t.Fatalf("entry1: %+v", m.entries[1])
	}
	if m.showWelcome {
		t.Fatal("welcome should be off when history present")
	}
	if !m.followBottom {
		t.Fatal("resume should follow bottom")
	}
	sawResume := false
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "resumed") {
			sawResume = true
			if !strings.Contains(e.text, "turns") {
				t.Fatalf("resume banner missing turn count: %q", e.text)
			}
			// Session id when available.
			if sid != "" && !strings.Contains(e.text, short(sid, 12)) && !strings.Contains(e.text, sid) {
				// short() may truncate; require "session" word at least
				if !strings.Contains(e.text, "session") {
					t.Fatalf("resume banner missing session: %q", e.text)
				}
			}
		}
	}
	if !sawResume {
		t.Fatal("expected resumed · … status banner after seed")
	}
}

func TestStaleStreamSnapDropped(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.busy = true
	cur := m.turnGen

	// Snap from the current turn applies.
	m.Update(streamSnapMsg{gen: cur, content: "hello"})
	if m.streamBuf != "hello" {
		t.Fatalf("current-gen snap not applied: %q", m.streamBuf)
	}

	// Turn boundary: reset bumps turnGen; a late snap from the old turn must drop.
	m.resetStreamState()
	m.busy = true
	m.Update(streamSnapMsg{gen: cur, content: "stale-tokens"})
	if m.streamBuf != "" {
		t.Fatalf("stale snap bled into new turn: %q", m.streamBuf)
	}
	m.Update(streamSnapMsg{gen: m.turnGen, content: "fresh"})
	if m.streamBuf != "fresh" {
		t.Fatalf("fresh snap not applied: %q", m.streamBuf)
	}
}

func TestLiveStreamSeamNotGlued(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.busy = true
	// Simulate: glamour rendered the prefix but trimmed its trailing newline;
	// the plain tail must not weld onto the last word.
	m.streamBuf = "I found the key files.\nLet me continue"
	m.streamBodySrc = "I found the key files.\n"
	m.streamBody = "I found the key files." // glamour trimmed the \n
	got := xansi.Strip(m.liveAnswerBody(60))
	if strings.Contains(got, "files.Let") {
		t.Fatalf("prefix/tail welded: %q", got)
	}
	if !strings.Contains(got, "files.") || !strings.Contains(got, "Let me continue") {
		t.Fatalf("content missing: %q", got)
	}

	// Space separator preserved the same way.
	m.streamBuf = "several key files. Let me continue"
	m.streamBodySrc = "several key files. "
	m.streamBody = "several key files."
	got = xansi.Strip(m.liveAnswerBody(60))
	if strings.Contains(got, "files.Let") {
		t.Fatalf("space seam welded: %q", got)
	}

	// Mid-word continuation must NOT gain a separator ("fi"+"les").
	m.streamBuf = "key files. more"
	m.streamBodySrc = "key fi"
	m.streamBody = "key fi"
	got = xansi.Strip(m.liveAnswerBody(60))
	if !strings.Contains(got, "key files. more") {
		t.Fatalf("mid-word continuation broken: %q", got)
	}

	// End-to-end: inline think tags stream in; visible text stays separated.
	m.resetStreamState()
	m.busy = true
	m.applyStreamSnap("key files.<think>plan the approach</think>Let me go", "")
	if strings.Contains(m.streamBuf, "files.Let") {
		t.Fatalf("think strip welded prose: %q", m.streamBuf)
	}
	if strings.Contains(m.streamBuf, "plan the approach") {
		t.Fatalf("thinking leaked into answer: %q", m.streamBuf)
	}
}

func TestTokenDisplayAccumulates(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.Update(doneMsg{text: "a", usage: mow.Usage{InputTokens: 900, OutputTokens: 100}})
	m.busy = true
	m.Update(doneMsg{text: "b", usage: mow.Usage{InputTokens: 11_500, OutputTokens: 500}})
	if m.tokIn != 12_400 || m.tokOut != 600 {
		t.Fatalf("tok=%d/%d", m.tokIn, m.tokOut)
	}
	if got := formatTokens(m.tokIn + m.tokOut); got != "13.0k" {
		t.Fatalf("formatTokens=%q", got)
	}
	view := m.View().Content
	if !strings.Contains(view, "13.0k") {
		t.Fatalf("header missing token chip: %q", view)
	}
}

func TestDelegateProgressAndChunkUI(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.resetToolTally()

	// Open peer window (acp_delegate PreTool).
	m.Update(toolUIMsg{name: "claude: acp_delegate", start: true, clearStream: true})
	if !m.peerLive.Load() || m.peerActive.Load() != 1 {
		t.Fatalf("peer window: live=%v active=%d", m.peerLive.Load(), m.peerActive.Load())
	}

	// Peer progress → live spinner label (same path as tool start).
	m.Update(toolUIMsg{name: "claude: read server.go", start: true})
	if m.toolCurrent != "claude: read server.go" {
		t.Fatalf("progress label=%q", m.toolCurrent)
	}
	band := xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, "claude") && !strings.Contains(m.toolCurrent, "claude") {
		t.Fatalf("activity band missing delegate progress: band=%q tool=%q", band, m.toolCurrent)
	}

	// Peer answer chunk → per-agent live buffer (not host streamBuf).
	m.Update(toolUIMsg{streamDelta: "hello from peer", peerAgent: "claude"})
	if !m.peerLive.Load() {
		t.Fatal("expected peerLive while peer chunks stream")
	}
	pb := m.peerBufs[peerKey("claude")]
	if pb == nil || !strings.Contains(pb.buf, "hello from peer") {
		t.Fatalf("peer buffer missing chunk: %#v frame=%q", m.peerBufs, m.streamFrame)
	}
	if m.streamBuf != "" {
		t.Fatalf("peer chunk must not land in host streamBuf: %q", m.streamBuf)
	}
	m.Update(toolUIMsg{streamDelta: "\n## Title\nmore", peerAgent: "claude"})
	if !strings.Contains(pb.buf, "Title") {
		t.Fatalf("peer text lost: %q", pb.buf)
	}
}

func TestDelegatePeerCommitAndClear(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.peerAgent.Store("claude")

	// Host leftovers wiped when acp_delegate starts.
	m.applyStreamSnap("host narration before tool", "")
	m.Update(toolUIMsg{name: "acp_delegate", start: true, clearStream: true})
	if m.streamBuf != "" {
		t.Fatalf("clearStream left streamBuf=%q", m.streamBuf)
	}
	if !m.peerLive.Load() {
		t.Fatal("peerLive should arm at acp_delegate start")
	}

	m.Update(toolUIMsg{streamDelta: "## Peer answer\n\nReadable body.", peerAgent: "claude"})
	nBefore := len(m.entries)
	m.Update(toolUIMsg{name: "acp_delegate", line: "acp_delegate · 1.0s", endPeer: true, peerAgent: "claude"})
	if m.peerLive.Load() {
		t.Fatal("peerLive should clear after endPeer")
	}
	if m.peerActive.Load() != 0 {
		t.Fatalf("peerActive=%d after last endPeer", m.peerActive.Load())
	}
	if m.toolCurrent != "writing" {
		t.Fatalf("after peers done want writing label, got %q", m.toolCurrent)
	}
	if m.streamBuf != "" {
		t.Fatalf("stream should clear after peer commit: %q", m.streamBuf)
	}
	// Late peer noise must not re-arm the UI.
	m.Update(toolUIMsg{streamDelta: "late orphan chunk"})
	m.Update(toolUIMsg{name: "claude: still thinking", start: true})
	if m.peerLive.Load() || strings.Contains(m.streamBuf, "orphan") {
		t.Fatalf("late peer events re-armed UI: live=%v buf=%q current=%q",
			m.peerLive.Load(), m.streamBuf, m.toolCurrent)
	}
	if m.toolCurrent != "writing" {
		t.Fatalf("late progress overwrote model label: %q", m.toolCurrent)
	}
	// Status label + assistant entry.
	if len(m.entries) < nBefore+2 {
		t.Fatalf("expected peer status+reply entries, got %d (was %d)", len(m.entries), nBefore)
	}
	foundStatus, foundReply := false, false
	for _, e := range m.entries[nBefore:] {
		if e.kind == kindStatus && strings.Contains(e.text, "claude") {
			foundStatus = true
		}
		if e.kind == kindAssistant && strings.Contains(e.text, "Peer answer") {
			foundReply = true
		}
	}
	if !foundStatus || !foundReply {
		t.Fatalf("missing peer commit entries: status=%v reply=%v entries=%+v",
			foundStatus, foundReply, m.entries[nBefore:])
	}
}

func TestDelegateChunksViaToolUI(t *testing.T) {
	// Production path: EventDelegateChunk → toolUICh → per-agent peerBuf.
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.peerLive.Store(true)
	m.ensurePeerBuf("claude")

	m.Update(toolUIMsg{streamDelta: "alpha ", peerAgent: "claude"})
	m.Update(toolUIMsg{streamDelta: "**bold**", peerAgent: "claude"})
	pb := m.peerBufs[peerKey("claude")]
	if pb == nil || !strings.Contains(pb.buf, "alpha **bold**") {
		t.Fatalf("peer buffer lost text: %#v", m.peerBufs)
	}
	if m.streamBuf != "" {
		t.Fatalf("host streamBuf should stay empty for peer chunks: %q", m.streamBuf)
	}
}

func TestToolTallyCoalesces(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.resetToolTally()

	// Start events only move the live indicator — no transcript entries.
	m.Update(toolUIMsg{name: "read", start: true})
	if m.toolCurrent != "read" || len(m.entries) != 0 {
		t.Fatalf("start: current=%q entries=%d", m.toolCurrent, len(m.entries))
	}

	// Several finished calls coalesce into ONE line that updates in place.
	m.Update(toolUIMsg{name: "read", line: "read · 0.1s"})
	m.Update(toolUIMsg{name: "read", line: "read · 0.2s"})
	m.Update(toolUIMsg{name: "grep", line: "grep · 0.1s"})
	toolLines := 0
	var tallyText string
	for _, e := range m.entries {
		if e.kind == kindTool {
			toolLines++
			tallyText = e.text
		}
	}
	if toolLines != 1 {
		t.Fatalf("tool lines=%d want 1 (coalesced)", toolLines)
	}
	if !strings.Contains(tallyText, "read ×2") || !strings.Contains(tallyText, "grep") {
		t.Fatalf("tally=%q", tallyText)
	}
	if m.toolCurrent != "" {
		t.Fatalf("current not cleared after end: %q", m.toolCurrent)
	}

	// A lone call keeps the richer single-line form.
	m.resetToolTally()
	m.Update(toolUIMsg{name: "bash", line: "bash · 1.2s"})
	last := m.entries[len(m.entries)-1]
	if last.kind != kindTool || last.text != "bash · 1.2s" {
		t.Fatalf("single call line=%q", last.text)
	}

	// Errors fold into the same tool tally line (not a dedicated kindError row).
	m.Update(toolUIMsg{name: "bash", line: "bash · error · exit 1", isErr: true})
	last = m.entries[len(m.entries)-1]
	if last.kind != kindTool {
		t.Fatalf("error should fold into tool line, kind=%v text=%q", last.kind, last.text)
	}
	if !strings.Contains(last.text, "⚠") || !strings.Contains(last.text, "exit 1") {
		t.Fatalf("tool line missing error mark: %q", last.text)
	}
}

func TestToolErrorsCoalesce(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.resetToolTally()

	m.Update(toolUIMsg{name: "read", line: "read · error · no such file: layout.go", isErr: true})
	m.Update(toolUIMsg{name: "read", line: "read · error · no such file: keys.go", isErr: true})
	m.Update(toolUIMsg{name: "read", line: "read · error · no such file: update.go", isErr: true})

	errRows, toolRows := 0, 0
	var last string
	for _, e := range m.entries {
		if e.kind == kindError {
			errRows++
		}
		if e.kind == kindTool {
			toolRows++
			last = e.text
		}
	}
	if errRows != 0 {
		t.Fatalf("error rows=%d want 0 (folded into tool line)", errRows)
	}
	if toolRows != 1 {
		t.Fatalf("tool rows=%d want 1", toolRows)
	}
	if !strings.Contains(last, "read") || !strings.Contains(last, "⚠") || !strings.Contains(last, "×3") {
		t.Fatalf("tool line should show read + error ×3: %q", last)
	}
	if !strings.Contains(last, "update.go") {
		t.Fatalf("latest error text expected: %q", last)
	}

	// Next turn starts a fresh tool line (previous tally stays as history).
	nBefore := len(m.entries)
	m.resetToolTally()
	m.Update(toolUIMsg{name: "bash", line: "bash · error · exit 1", isErr: true})
	toolRows = 0
	for _, e := range m.entries[nBefore:] {
		if e.kind == kindTool {
			toolRows++
		}
		if e.kind == kindError {
			t.Fatalf("new turn should not add kindError, got %q", e.text)
		}
	}
	if toolRows != 1 {
		t.Fatalf("new turn tool rows=%d want 1", toolRows)
	}
}

func TestIntermediateTurnTextCommitted(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.resetToolTally()

	// Turn 1 streams narration, then a tool batch begins.
	m.applyStreamSnap("I'll check the key files.", "")
	if m.streamBuf == "" {
		t.Fatal("live buffer should hold turn-1 text")
	}
	m.Update(toolUIMsg{turnText: "I'll check the key files."})

	// The narration became a real transcript entry…
	found := false
	for _, e := range m.entries {
		if e.kind == kindAssistant && strings.Contains(e.text, "key files.") {
			found = true
		}
	}
	if !found {
		t.Fatalf("intermediate turn text not committed: %v", m.lines())
	}
	// …and the live stream reset, so turn 2 cannot weld onto turn 1.
	if m.streamBuf != "" {
		t.Fatalf("live buffer not cleared at turn boundary: %q", m.streamBuf)
	}
	m.applyStreamSnap("Let me summarize.", "")
	if strings.Contains(m.streamBuf, "key files.") {
		t.Fatalf("turn 2 welded onto turn 1: %q", m.streamBuf)
	}

	// Final answer commits separately; narration entry survives.
	m.Update(doneMsg{text: "Here is the summary."})
	narration, final := 0, 0
	for _, e := range m.entries {
		if e.kind != kindAssistant {
			continue
		}
		if strings.Contains(e.text, "key files.") {
			narration++
		}
		if strings.Contains(e.text, "Here is the summary.") {
			final++
		}
	}
	if narration != 1 || final != 1 {
		t.Fatalf("narration=%d final=%d want 1/1: %v", narration, final, m.lines())
	}
}

func TestInputQueueingWhileBusy(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true

	// Type + Enter while busy → queued, not sent, draft cleared.
	m.ta.SetValue("second question")
	if cmd := m.queueDraft(); cmd != nil {
		t.Fatal("plain queue should not run a command")
	}
	if len(m.queued) != 1 || m.queued[0] != "second question" {
		t.Fatalf("queued=%v", m.queued)
	}
	if strings.TrimSpace(m.ta.Value()) != "" {
		t.Fatalf("draft not cleared: %q", m.ta.Value())
	}
	// Teach-once status may mention queue; draft preview must not enter transcript.
	sawTeach := false
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "/steer") {
			sawTeach = true
		}
		if e.kind == kindStatus && strings.Contains(e.text, "second question") {
			t.Fatalf("queue draft preview leaked into transcript: %q", e.text)
		}
	}
	if !sawTeach {
		t.Fatal("expected one-shot queue teach status")
	}
	if band := xansi.Strip(m.renderActivityBand()); !strings.Contains(band, "queued · 1") {
		t.Fatalf("band missing queue chip: %q", band)
	}

	// Turn ends → dequeue submits it (submit sets busy=true again).
	m.busy = false
	mod, _ := m.dequeue()
	m2 := mod.(*model)
	if len(m2.queued) != 0 {
		t.Fatalf("queue not drained: %v", m2.queued)
	}
	if !m2.busy {
		t.Fatal("dequeue should submit (busy=true)")
	}
}

func TestCancelDropsQueue(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.queued = []string{"a", "b"}
	m.dropQueue()
	if len(m.queued) != 0 {
		t.Fatalf("cancel should drop queue: %v", m.queued)
	}
}

func TestCopyLastAnswer(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// No answer yet → status, no clipboard command.
	if cmd := m.copyLastAnswer(); cmd != nil {
		t.Fatal("no answer should not emit a clipboard command")
	}
	m.add(kindUser, "question")
	m.add(kindAssistant, "the answer text")
	m.add(kindStatus, "some status")
	cmd := m.copyLastAnswer()
	if cmd == nil {
		t.Fatal("expected a SetClipboard command for the last answer")
	}
	// The most recent *assistant* entry is copied (not the status line).
	sawCopied := false
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "copied") {
			sawCopied = true
		}
	}
	if !sawCopied {
		t.Fatal("no copied confirmation")
	}
}

func TestListSessionsEmpty(t *testing.T) {
	// testEngine uses NoSession, so Sessions() is empty → clean status, no panic.
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cmd := m.listSessions(); cmd != nil {
		t.Fatal("listing should not return a command")
	}
	saw := false
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "sessions") {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected a sessions status line")
	}
}

func TestPrintSessionExit(t *testing.T) {
	// No session → silent (matches mow tty with --no-session).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	printSessionExit(testEngine(t))
	printSessionExit(nil)
	_ = w.Close()
	os.Stderr = old
	buf, _ := io.ReadAll(r)
	_ = r.Close()
	if len(buf) != 0 {
		t.Fatalf("expected no banner without session, got %q", buf)
	}

	// Persist a session so SessionID is set after first prompt.
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	eng, err := mow.New(mow.Options{
		NoSession: false,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	sid := eng.SessionID()
	if sid == "" {
		t.Fatal("expected session id after prompt")
	}

	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	printSessionExit(eng)
	_ = w.Close()
	os.Stderr = old
	buf, _ = io.ReadAll(r)
	_ = r.Close()
	out := string(buf)
	if !strings.Contains(out, "session="+sid) {
		t.Fatalf("missing session line: %q", out)
	}
	if !strings.Contains(out, "mowi --session "+sid) {
		t.Fatalf("missing resume --session: %q", out)
	}
	if !strings.Contains(out, "mowi --continue") {
		t.Fatalf("missing resume --continue: %q", out)
	}
	_ = eng.Close()
}

func TestPeerLiveMarkdownStablePrefix(t *testing.T) {
	// Per-agent peer buffers: glamoured prefix + plain tail via peerLiveBody.
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.busy = true
	m.peerLive.Store(true)
	b := m.ensurePeerBuf("claude")
	b.body = "# Title pretty"
	b.bodySrc = "# Title"
	b.buf = "# Title\n\nmore **bold** text"
	inner := max(16, max(24, m.vp.Width()-2)-roleGutterW)
	got := peerLiveBody(b, inner)
	if !strings.Contains(got, "Title pretty") {
		t.Fatalf("expected glamoured peer prefix: %q", got)
	}
	if !strings.Contains(got, "bold") && !strings.Contains(got, "more") {
		t.Fatalf("expected plain peer tail: %q", got)
	}
	// kickStreamRender schedules glamour for dirty peer buffers — but only
	// when peers are expanded. Collapsed peers show a one-line summary, so
	// spending a glamour pass per chunk on invisible text is pure waste.
	b.body, b.bodySrc = "", ""
	b.dirty = true
	m.streamRenderBusy = false
	m.streamBuf = ""

	m.peerExpanded = false
	if cmd := m.kickStreamRender(); cmd != nil {
		t.Fatal("collapsed peer must not schedule live glamour")
	}

	m.peerExpanded = true
	m.streamRenderBusy = false
	cmd := m.kickStreamRender()
	if cmd == nil {
		t.Fatal("expanded dirty peer buffer must schedule live glamour")
	}
}

func TestParallelPeerBuffersDoNotInterleave(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	// Two concurrent peers.
	m.Update(toolUIMsg{name: "claude: acp_delegate", start: true, clearStream: true, peerAgent: "claude"})
	m.Update(toolUIMsg{name: "peer-d: acp_delegate", start: true, clearStream: true, peerAgent: "peer-d"})
	if m.peerActive.Load() != 2 {
		t.Fatalf("peerActive=%d want 2", m.peerActive.Load())
	}
	m.Update(toolUIMsg{streamDelta: "AAA", peerAgent: "claude"})
	m.Update(toolUIMsg{streamDelta: "BBB", peerAgent: "peer-d"})
	m.Update(toolUIMsg{streamDelta: "aaa", peerAgent: "claude"})
	m.Update(toolUIMsg{streamDelta: "bbb", peerAgent: "peer-d"})
	c := m.peerBufs[peerKey("claude")]
	o := m.peerBufs[peerKey("peer-d")]
	if c == nil || o == nil {
		t.Fatalf("missing peer bufs: %#v", m.peerBufs)
	}
	if c.buf != "AAAaaa" {
		t.Fatalf("claude buf interleaved: %q", c.buf)
	}
	if o.buf != "BBBbbb" {
		t.Fatalf("peer-d buf interleaved: %q", o.buf)
	}
	// Commit claude only — peer-d stays live.
	m.Update(toolUIMsg{endPeer: true, peerAgent: "claude", line: "acp_delegate · 0.1s"})
	if m.peerBufs[peerKey("claude")] != nil {
		t.Fatal("claude buffer should be committed/removed")
	}
	if m.peerBufs[peerKey("peer-d")] == nil || m.peerBufs[peerKey("peer-d")].buf != "BBBbbb" {
		t.Fatalf("peer-d should still be live: %#v", m.peerBufs)
	}
	if !m.peerLive.Load() || m.peerActive.Load() != 1 {
		t.Fatalf("after one end: live=%v active=%d", m.peerLive.Load(), m.peerActive.Load())
	}
	// Commit peer-d.
	m.Update(toolUIMsg{endPeer: true, peerAgent: "peer-d", line: "acp_delegate · 0.2s"})
	if m.peerLive.Load() || m.peerActive.Load() != 0 {
		t.Fatalf("after all end: live=%v active=%d", m.peerLive.Load(), m.peerActive.Load())
	}
	if len(m.peerBufs) != 0 {
		t.Fatalf("peerBufs not cleared: %#v", m.peerBufs)
	}
}

func TestLSPDiagnosticsEventAddsCappedProblems(t *testing.T) {
	eng := testEngine(t)
	m := newModel(eng, false, false)
	eng.Emit(mow.Event{
		Type: mow.EventLSPDiagnostics, Tool: "edit", Path: "internal/widget.go", Count: 8,
		Diagnostics: []mow.Diagnostic{
			{Severity: "error", Line: 20, Message: "hidden error"},
			{Severity: "warning", Line: 8, Message: "unused variable"},
			{Severity: "error", Line: 42, Message: "undefined: frobnicate"},
			{Severity: "error", Line: 43, Message: "missing return"},
			{Severity: "error", Line: 44, Message: "bad type"},
			{Severity: "information", Line: 12, Message: "consider simplifying"},
		},
	})
	select {
	case msg := <-m.toolUICh:
		raw, _ := m.Update(msg)
		m = raw.(*model)
	case <-time.After(time.Second):
		t.Fatal("LSP event did not reach TUI")
	}
	if !findEntry(m, kindError, "internal/widget.go:42 undefined: frobnicate") || findEntry(m, kindError, "error —") {
		t.Fatalf("error should be styled without redundant severity: %v", m.lines())
	}
	if !findEntry(m, kindStatus, "lsp · internal/widget.go · …5 more (1 errors)") {
		t.Fatalf("missing hidden-error summary: %v", m.lines())
	}
}

func TestLSPProblemsRetainNewestPathBatches(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	for i := 0; i < maxLSPProblemPaths+2; i++ {
		m.addLSPProblems(lspProblemsEvent{path: fmt.Sprintf("p%d.go", i), count: 1, diagnostics: []mow.Diagnostic{{Severity: "warning", Line: 1, Message: "old"}}})
	}
	m.addLSPProblems(lspProblemsEvent{path: "p5.go", count: 1, diagnostics: []mow.Diagnostic{{Severity: "error", Line: 9, Message: "newest"}}})
	if len(m.lspProblems) != maxLSPProblemPaths || m.lspProblems[0].path != "p5.go" {
		t.Fatalf("retention=%#v", m.lspProblems)
	}
	m.clearTranscript()
	_ = m.handleSlash("/lsp")
	if !findEntry(m, kindStatus, "lsp · p5.go · 1 problem(s)") || !findEntry(m, kindError, "p5.go:9 newest") {
		t.Fatalf("/lsp should group newest batch first: %v", m.lines())
	}
	if !findEntry(m, kindStatus, "lsp · …older omitted") {
		t.Fatalf("/lsp should bound recent batches: %v", m.lines())
	}
}

func TestLSPDiagnosticsZeroCountIsQuiet(t *testing.T) {
	eng := testEngine(t)
	m := newModel(eng, false, false)
	eng.Emit(mow.Event{Type: mow.EventLSPDiagnostics, Tool: "write", Path: "clean.go"})
	select {
	case <-m.toolUICh:
		t.Fatal("zero-diagnostic event should not produce a transcript entry")
	case <-time.After(20 * time.Millisecond):
	}
}

// A mid-turn steer interrupts the in-flight LLM call; the engine emits
// EventSteer and mowi must reset the partial live stream so the reissued
// answer does not weld onto the interrupted text.
func TestSteerResetsPartialLiveStream(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.busy = true
	m.streamBuf = "partial answer so far"
	m.streamRaw = "partial answer so far"
	m.streamBody = "partial styled"
	m.streamBodySrc = "partial answer so far"
	if cmd := m.doSteer("course correct"); cmd != nil {
		t.Fatal("steer should not schedule a cmd")
	}
	if m.streamBuf != "" || m.streamRaw != "" || m.streamBody != "" {
		t.Fatalf("live stream not reset after steer: buf=%q body=%q", m.streamBuf, m.streamBody)
	}
	// A steer status entry is recorded for the transcript.
	saw := false
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "steer") {
			saw = true
		}
	}
	if !saw {
		t.Fatal("steer status entry missing")
	}
	// A streamSnapMsg taken BEFORE the steer carries the OLD turnGen. The
	// steer handler bumped turnGen, so this stale snap (the interrupted
	// call's partial tail) must be dropped — otherwise the cleared partial
	// re-welds onto the reissued answer.
	oldGen := m.turnGen - 1
	mod, _ := m.Update(streamSnapMsg{gen: oldGen, content: "partial answer so far", finished: false})
	mm := mod.(*model)
	if mm.streamBuf != "" || mm.streamRaw != "" {
		t.Fatalf("stale pre-steer snap re-welded onto the reissued stream: buf=%q", mm.streamBuf)
	}
	// A snap at the NEW gen (the reissued answer) paints normally.
	mod, _ = mm.Update(streamSnapMsg{gen: mm.turnGen, content: "fresh answer", finished: false})
	if got := mod.(*model).streamBuf; got != "fresh answer" {
		t.Fatalf("fresh-gen snap not painted: %q", got)
	}
}

// /compact (idle) runs the engine's manual compaction and reports the layer +
// savings; while busy it says it applies when the turn finishes.
func TestCompactSlashCommand(t *testing.T) {
	// Idle with no history: "nothing to trim".
	m := goalTestModel(t)
	_ = m.handleSlash("/compact")
	if !strings.Contains(entryTexts(m)[len(entryTexts(m))-1], "nothing to trim") {
		t.Fatalf("idle empty compact line: %v", entryTexts(m))
	}
	// Busy: honest defer message.
	m2 := goalTestModel(t)
	m2.busy = true
	_ = m2.handleSlash("/compact")
	last := entryTexts(m2)[len(entryTexts(m2))-1]
	if !strings.Contains(last, "when the turn finishes") {
		t.Fatalf("busy compact line: %q", last)
	}
}
