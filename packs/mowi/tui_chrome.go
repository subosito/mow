package mowi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/subosito/mow/slash"
)

// spinnerView is the busy spinner, or a static glyph under reduced peer-bion.
func (m *model) spinnerView() string {
	if reducedMotion() {
		return m.theme.Accent.Render(glyphBrand)
	}
	return m.spin.View()
}

// syncInputChrome sets prompt prefix + colors.
// Busy: spinner + always-visible elapsed (e.g. "2.3s") so long TTFT still
// feels alive — the counter is the heartbeat even if a spinner frame stalls.
// Idle: ❯ or slash amber.
func (m *model) syncInputChrome() {
	st := m.ta.Styles()
	if m.busy {
		// Busy prompt is a short compose cue only — live tool/peer detail
		// lives on the activity band above the input rule (not here).
		var prompt string
		if reducedMotion() {
			prompt = m.theme.Accent.Render(glyphBrand)
		} else {
			prompt = m.theme.Muted.Render("…")
		}
		if len(m.queued) > 0 {
			prompt = m.theme.Muted.Render("…")
		}
		m.ta.Prompt = prompt + " "
		st.Focused.Prompt = lipgloss.NewStyle()
		st.Blurred.Prompt = lipgloss.NewStyle()
		// Dim draft text while a permission prompt owns the mode.
		if m.permWait != nil {
			st.Focused.Text = m.theme.Muted
		} else {
			st.Focused.Text = m.inputTextColor
		}
		st.Cursor.Color = m.inputPrompt.GetForeground()
		m.ta.SetStyles(st)
		return
	}
	m.ta.Prompt = m.cfg.PromptPrefix()
	switch {
	case m.editingPrompt:
		// Edit mode (arrow-up recall / /edit): accent prompt + tag so the
		// state is obvious; Esc cancels back to a blank prompt.
		m.ta.Prompt = m.theme.Accent.Render("edit ❯") + " "
		st.Focused.Text = m.inputTextColor
		st.Focused.Prompt = m.theme.Accent
		st.Cursor.Color = m.theme.Accent.GetForeground()
	case m.isSlashInput():
		st.Focused.Text = m.slashTextColor
		st.Focused.Prompt = m.slashPrompt
		st.Cursor.Color = m.slashPrompt.GetForeground()
	default:
		st.Focused.Text = m.inputTextColor
		st.Focused.Prompt = m.inputPrompt
		st.Cursor.Color = m.inputPrompt.GetForeground()
	}
	m.ta.SetStyles(st)
}

// busyHeartbeatInterval advances spinner frame + elapsed while a turn runs.
// Own chain — do not rely on bubbles spinner's tag-based Tick reschedule
// (a mismatched tag returns nil Cmd and the animation dies permanently).
const busyHeartbeatInterval = 100 * time.Millisecond

func (m *model) scheduleBusyHeartbeat() tea.Cmd {
	return tea.Tick(busyHeartbeatInterval, func(time.Time) tea.Msg {
		return busyHeartbeatMsg{}
	})
}

// advanceSpinnerFrame steps the spinner once. Uses tag=0 so bubbles never
// rejects the message; discards bubbles' own follow-up Tick cmd.
func (m *model) advanceSpinnerFrame() {
	if reducedMotion() {
		return
	}
	// tag 0 skips the "wrong tag → drop" check in bubbles/spinner.Update.
	m.spin, _ = m.spin.Update(spinner.TickMsg{
		Time: time.Now(),
		ID:   m.spin.ID(),
	})
}

// formatTokens is compact token display: 950, 12.3k, 1.2M.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatContextPct renders last-call input tokens vs gateway context_window.
// Returns warn=true at ≥80%. Avoids integer-floor "0% ctx" on large windows.
func formatContextPct(used, window int) (label string, warn bool) {
	label, level := formatContextPctLevel(used, window)
	return label, level >= 2
}

// formatContextPctLevel: 0 muted, 1 attention (≥50%), 2 warn (≥80%).
func formatContextPctLevel(used, window int) (label string, level int) {
	if used <= 0 || window <= 0 {
		return "", 0
	}
	ratio := float64(used) * 100 / float64(window)
	switch {
	case ratio >= 80:
		level = 2
	case ratio >= 50:
		level = 1
	default:
		level = 0
	}
	switch {
	case ratio < 0.1:
		return "<0.1% ctx", level
	case ratio < 1:
		return fmt.Sprintf("%.1f%% ctx", ratio), level
	default:
		return fmt.Sprintf("%.0f%% ctx", ratio), level
	}
}

// formatElapsed is a compact always-on busy timer:
//
//	0.0s … 9.9s  → tenths
//	10s … 59s    → whole seconds
//	1m+          → 1m 05s, 10m 00s, …
//	1h+          → 1h 2m, 1h 2m 03s, …
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	total := int(d.Round(time.Second) / time.Second)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	// Dense chrome: no spaces between units (6m05s, 1h02m03s).
	if total < 3600 {
		return fmt.Sprintf("%dm%02ds", total/60, total%60)
	}
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	switch {
	case m == 0 && s == 0:
		return fmt.Sprintf("%dh", h)
	case s == 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m == 0:
		return fmt.Sprintf("%dh%02ds", h, s)
	default:
		return fmt.Sprintf("%dh%dm%02ds", h, m, s)
	}
}

// maybeCtxPressureStatus emits a one-shot status when session context is high.
func (m *model) maybeCtxPressureStatus() {
	limits := m.eng.Limits()
	if limits.ContextWindow <= 0 {
		return
	}
	ct := m.eng.ContextTokens()
	if ct <= 0 {
		// Cumulative usage re-counts prior context on every call and is not a
		// context-size estimate. With no latest-call input count, suppress the
		// warning rather than manufacture a misleading percentage.
		return
	}
	_, level := formatContextPctLevel(ct, limits.ContextWindow)
	if level < 2 || m.ctxPressureBand >= level {
		return
	}
	m.ctxPressureBand = level
	label, _ := formatContextPct(ct, limits.ContextWindow)
	m.add(kindStatus, label+" — consider a new session or shorter context")
}

// ctxGaugeWidth is the cell width of the context-pressure bar (excluding the
// numeric label). Small on purpose: the header is a priority-dropped chip line,
// so the gauge must read at a glance without competing for width with the
// safety chips it sits beside.
const ctxGaugeWidth = 6

// ctxGaugeMinWidth is the terminal width below which the gauge is suppressed
// entirely. Narrow terminals need every column for the identity group and the
// safety chips; the numeric percentage alone carries the signal there.
const ctxGaugeMinWidth = 100

// ctxGauge renders a compact fill bar for context-window pressure, matched to
// the level thresholds used by formatContextPctLevel (muted <50%, attention
// ≥50%, warn ≥80%). Rendered with ViewAs so it is a pure function of the ratio:
// no animation state, no Update wiring, and no frame Cmds to schedule.
//
// Returns "" when the ratio is unusable, so callers fall back to the numeric
// label alone. The bar is additive — the percentage stays the source of truth.
func (m *model) ctxGauge(used, window int) string {
	if used <= 0 || window <= 0 || ctxGaugeWidth <= 0 {
		return ""
	}
	ratio := float64(used) / float64(window)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	_, level := formatContextPctLevel(used, window)
	fill := m.theme.Muted.GetForeground()
	switch level {
	case 2:
		fill = m.theme.Warn.GetForeground()
	case 1:
		fill = m.theme.Accent.GetForeground()
	}
	p := progress.New(
		progress.WithWidth(ctxGaugeWidth),
		progress.WithoutPercentage(),
		progress.WithFillCharacters(glyphGaugeFull, glyphGaugeEmpty),
		progress.WithColors(fill),
	)
	return p.ViewAs(ratio)
}

func (m *model) View() tea.View {
	var content string
	switch {
	case m.quitting:
		content = ""
	case !m.ready:
		content = m.theme.Muted.Render(" mowi ")
	case m.tooSmall():
		content = m.sizeWarnView()
	default:
		content = m.mainFrame()
		if m.effortPick != nil {
			content = placeOverlayCenter(m.effortPickerCard(), content, max(1, m.width), max(1, m.height))
		} else if m.modelPick != nil {
			content = placeOverlayCenter(m.modelPickerCard(), content, max(1, m.width), max(1, m.height))
		} else if m.showHelp {
			// Overlay help card on the live frame so transcript stays visible.
			content = placeOverlayCenter(m.helpCard(), content, max(1, m.width), max(1, m.height))
		}
	}
	v := tea.NewView(content)
	// BT v2: declare terminal features on the View (not NewProgram options).
	v.AltScreen = true
	// Mouse tracking steals the mouse from the terminal: drag-to-select text
	// is traded for wheel scroll. On by default so the wheel reaches the
	// transcript as a MouseWheelMsg instead of translated arrow keys, which
	// would recall or edit the prompt in the input box. MOW_MOUSE=0 opts out
	// and restores native selection; ctrl+u / ctrl+d still scroll either way.
	v.MouseMode = tea.MouseModeNone
	if m.mouseOn() {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// mouseTrackingEnabled reports whether the app owns the mouse for wheel
// scroll. On by default: without tracking, terminals translate wheel events
// into arrow-key sequences, and a wheel-up would recall the last prompt into
// the input (or a wheel-down would move/clear it) instead of scrolling the
// transcript. Set MOW_MOUSE=0 (also false/off/no) to restore native terminal
// selection — keys ctrl+u / ctrl+d still scroll, and the arrow-burst guard
// keeps wheel noise out of the prompt.
func mouseTrackingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MOW_MOUSE"))) {
	case "0", "false", "off", "no", "none":
		return false
	}
	return true
}

// mouseOn reports whether mouse tracking is currently active. MOW_MOUSE sets
// the starting value; the select-mode toggle flips it at runtime so a user can
// grab the mouse back for copy/paste without restarting and losing the session.
func (m *model) mouseOn() bool {
	if m.mouseOff {
		return false
	}
	return mouseTrackingEnabled()
}

// tooSmall reports a terminal too cramped for usable chrome.
func (m *model) tooSmall() bool {
	return m.width > 0 && m.height > 0 &&
		(m.width < minTermWidth || m.height < minTermHeight)
}

func (m *model) sizeWarnView() string {
	msg := m.theme.Warn.Render(fmt.Sprintf(
		"terminal too small\n%d×%d  need ≥ %d×%d",
		m.width, m.height, minTermWidth, minTermHeight,
	))
	return lipgloss.Place(max(1, m.width), max(1, m.height), lipgloss.Center, lipgloss.Center, msg)
}

// showEmptyState reports whether the faint placeholder should occupy the
// viewport: welcome dismissed, idle, and no transcript entries yet. The first
// entry (user prompt, status, error…) clears it naturally.
func (m *model) showEmptyState() bool {
	return !m.showWelcome && !m.busy && len(m.entries) == 0
}

// mainFrame is header | transcript | [activity] | [permission] | input.
func (m *model) mainFrame() string {
	main := m.vp.View()
	if m.showWelcome {
		main = m.welcomeView()
	} else if m.showEmptyState() {
		// After the splash is dismissed but before the first turn, keep a faint
		// placeholder so the viewport is not a blank void. Disappears as soon
		// as any entry lands (submit, status, error, …).
		main = m.emptyStateView()
	}
	// Scroll indicators are overlays inside the viewport's fixed height.
	// Appending a row here made mainFrame one line taller than layout() budgeted,
	// pushing the first transcript/user-prompt row above the terminal frame.
	if !m.showWelcome && !m.showEmptyState() {
		indicator := ""
		if !m.followBottom && m.busy && (m.streamBuf != "" || m.toolCurrent != "") {
			indicator = m.theme.Muted.Render("↓ new output · end/pgdn to follow")
		} else if !m.followBottom && m.vp.TotalLineCount() > m.vp.VisibleLineCount() {
			pct := max(0, min(100, int(m.vp.ScrollPercent()*100)))
			indicator = m.theme.Muted.Faint(true).Render(fmt.Sprintf("↑ %d%%", pct))
		}
		if indicator != "" {
			alignRight := !m.busy || (m.streamBuf == "" && m.toolCurrent == "")
			main = overlayViewportFooter(main, indicator, m.width, m.vp.Height(), alignRight)
		}
	}
	parts := []string{m.renderHeader(), main}
	if band := m.renderActivityBand(); band != "" {
		parts = append(parts, band)
	}
	if act := m.renderPermissionStrip(); act != "" {
		parts = append(parts, act)
	}
	parts = append(parts, m.renderInput())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func overlayViewportFooter(main, indicator string, width, height int, alignRight bool) string {
	if height <= 0 || strings.TrimSpace(indicator) == "" {
		return main
	}
	lines := strings.Split(main, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	row := height - 1
	pad := max(0, (max(1, width)-lipgloss.Width(indicator))/2)
	if alignRight {
		pad = max(0, max(1, width)-lipgloss.Width(indicator))
	}
	lines[row] = strings.Repeat(" ", pad) + indicator
	return strings.Join(lines, "\n")
}

// compensateBandScroll keeps transcript content stable when the activity band
// toggles height (absent when idle).
func (m *model) compensateBandScroll(nowOn bool) {
	if !m.ready {
		return
	}
	if m.followBottom {
		// Pin after layout sets height.
		return
	}
	delta := activityBandRows
	if nowOn {
		// Band appears → viewport shrinks; keep top lines stable by
		// reducing YOffset when possible.
		y := m.vp.YOffset()
		if y >= delta {
			m.vp.SetYOffset(y - delta)
		}
		return
	}
	// Band disappears → viewport grows; push offset down so content doesn't jump.
	m.vp.SetYOffset(m.vp.YOffset() + delta)
}

func (m *model) welcomeView() string {
	th := m.theme
	var full string
	if strings.TrimSpace(m.cfg.WelcomeMessage) != "" {
		// Respect a configured splash verbatim (soft, no chrome).
		block := th.Muted.Render(m.cfg.WelcomeText())
		full = m.welcomeHints(block)
	} else {
		// Branded welcome: wordmark + plain-language trust/value copy + live
		// model context + a few useful example prompts. The header carries
		// workspace and safety posture; the welcome explains what mowi can do
		// and how to begin, so a cold user has a path on the first screen.
		brand := th.Title.Render(glyphWelcome + " mowi")
		tagline := th.Muted.Render("agentic coding in your terminal")
		ctx := th.Muted.Faint(true).Render(
			short(m.eng.Model(), 32),
		)
		// Start compact: brand + tagline. Model context, trust, and examples
		// are added progressively when the viewport has room.
		block := lipgloss.JoinVertical(lipgloss.Center, brand, "", tagline)

		// Trust line + examples are progressive: they need viewport room. At a
		// minimum-size terminal (40×10 → ~6 viewport rows) the full block would
		// overflow, so only the brand/tagline + hints fit. Show the model
		// context, trust, and examples only when there is enough vertical room.
		vh := m.vp.Height()
		if vh <= 0 {
			vh = max(1, m.height-5)
		}
		if vh >= 9 {
			block = lipgloss.JoinVertical(lipgloss.Center, block, "", ctx)
		}
		if vh >= 12 {
			trust := m.welcomeTrustLine()
			block = lipgloss.JoinVertical(lipgloss.Center, block, "", trust)

			// Examples: short, repo-shaped prompts a new user can adapt. These are
			// static (not workspace-probed) so they stay generic and OSS-clean.
			// One per line so they never overflow at minTermWidth (40 cols).
			examples := []string{
				"Explain this repository",
				"Find why a test is failing",
				"Plan a safe refactor",
			}
			var ex strings.Builder
			for i, e := range examples {
				if i > 0 {
					ex.WriteString("\n")
				}
				ex.WriteString(th.Muted.Render(glyphBullet + " " + e))
			}
			block = lipgloss.JoinVertical(lipgloss.Center, block, "", "", ex.String())
		}

		full = m.welcomeHints(block)
	}
	h := m.vp.Height()
	if h < 1 {
		h = max(1, m.height-5)
	}
	// Clamp content width to the viewport so lipgloss.Place never produces a
	// line wider than the terminal (which would soft-wrap and break chrome).
	full = clampFrameLines(full, max(1, m.width))
	return lipgloss.Place(max(1, m.width), h, lipgloss.Center, lipgloss.Center, full)
}

// welcomeHints renders the discoverability footer line (keys + dismiss).
// Keys come from the *resolved* keymap so config overrides stay accurate
// (same contract as helpCard).
func (m *model) welcomeHints(block string) string {
	th := m.theme
	helpKey := m.cfg.Keys.Primary(m.cfg.Keys.Help)
	if helpKey == "" {
		helpKey = "?"
	}
	cancelKey := m.cfg.Keys.Primary(m.cfg.Keys.Cancel)
	if cancelKey == "" {
		cancelKey = "esc"
	}
	hint := th.Muted.Faint(true).Render(
		"type a message to start  " + glyphBullet + "  " + helpKey + " help  " + glyphBullet + "  " + cancelKey + " dismiss",
	)
	return lipgloss.JoinVertical(lipgloss.Center, block, "", "", hint)
}

// welcomeTrustLine renders the one-line trust/capability summary for the
// branded welcome. It distinguishes:
//   - Capability (AllowWrite/AllowShell): whether edit/bash tools exist at all.
//     False = tool disabled entirely (not merely gated). Default read-only
//     engine has both off, so the model cannot edit or run commands — period.
//   - Approval mode (perm() == PermAsk or autoPower): when a capability is on,
//     whether each use prompts the user before running. autoPower ("always
//     allow") means prompts are skipped.
//
// Plain, concise language; no jargon like "auto" or "ask".
func (m *model) welcomeTrustLine() string {
	th := m.theme
	w, s := m.eng.AllowWrite(), m.eng.AllowShell()
	var cap string
	switch {
	case w && s:
		cap = "can read, edit, and run commands"
	case w:
		cap = "can read and edit files"
	case s:
		cap = "can read files and run commands"
	default:
		cap = "read-only — editing and commands are off"
	}
	// Approval behavior, only when a capability is on. PermAsk = prompts before
	// each use; autoPower = "always allow" override (no prompts). When neither
	// capability is on, approval is irrelevant and omitted.
	var approval string
	if w || s {
		switch {
		case m.autoPower.Load():
			approval = " · changes run without asking"
		case m.perm() == PermAsk:
			approval = " · asks before each change"
		default:
			approval = " · changes run automatically"
		}
	}
	return th.Muted.Faint(true).Render(cap + approval)
}

// emptyStateView is the faint placeholder shown in the viewport when the
// welcome splash has been dismissed but no turns have happened yet. It keeps
// the first screen from reading as a blank void, then disappears on the first
// input. Plain + static so it never competes with real transcript content.
func (m *model) emptyStateView() string {
	th := m.theme
	helpKey := m.cfg.Keys.Primary(m.cfg.Keys.Help)
	if helpKey == "" {
		helpKey = "?"
	}
	examples := []string{
		"Explain this repository",
		"Find why a test is failing",
		"Plan a safe refactor",
	}
	var b strings.Builder
	b.WriteString(th.Muted.Faint(true).Render("type a question, or " + helpKey + " for help") + "\n\n")
	for _, e := range examples {
		b.WriteString(th.Muted.Faint(true).Render(glyphBullet+" "+e) + "\n")
	}
	h := m.vp.Height()
	if h < 1 {
		h = max(1, m.height-5)
	}
	return lipgloss.Place(max(1, m.width), h, lipgloss.Center, lipgloss.Center, strings.TrimRight(b.String(), "\n"))
}

func (m *model) reportedUsageStatus() string {
	total := m.tokIn + m.tokOut + m.peerTokIn + m.peerTokOut
	if total <= 0 {
		return ""
	}
	lines := []string{
		"usage reported this run · " + formatTokens(total) + " total",
		"  host · " + formatTokens(m.tokIn) + " in · " + formatTokens(m.tokOut) + " out",
	}
	if peer := m.peerTokIn + m.peerTokOut; peer > 0 {
		lines = append(lines, "  peers · "+formatTokens(m.peerTokIn)+" in · "+formatTokens(m.peerTokOut)+" out")
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderHeader() string {
	// Quiet header, exactly 2 rows (clamped so soft-wrap cannot steal a row):
	//   left  = wordmark + workspace dir + active model/effort (identity, stable)
	//   right = priority-dropped chips (safety never truncated away)
	th := m.theme
	dot := th.Muted.Faint(true).Render(" " + glyphBullet + " ")
	left := th.Accent.Render("mowi")
	// Workspace comes first as quiet context; model is the strongest, rightmost
	// item in the identity group. Effort is adjacent but muted, so it reads as
	// model metadata without another slash/separator competing with the header.
	if ws := short(filepath.Base(m.eng.Workspace()), 20); ws != "" && ws != "." && ws != "/" {
		left += dot + th.Muted.Render(ws)
	}
	if mdl := short(strings.TrimSpace(m.eng.Model()), 32); mdl != "" {
		left += dot + th.Text.Render(mdl)
		if effort := short(strings.TrimSpace(m.eng.Effort()), 12); effort != "" {
			left += " " + th.Muted.Render("("+effort+")")
		}
	}

	type chip struct {
		text string
		must bool // never drop while present
	}
	var must, vanity []chip

	// Safety first (must): capability posture, always visible. Elevated powers
	// get warn chips (▲ write / ▲ shell = tools the model CAN use, not what it
	// is doing); the safe default shows a quiet "read only" so the posture is
	// never ambiguous — silence must never mean "fine".
	if m.eng.AllowWrite() {
		must = append(must, chip{th.Warn.Render(glyphWarn + " write"), true})
	}
	if m.eng.AllowShell() {
		must = append(must, chip{th.Warn.Render(glyphWarn + " shell"), true})
	}
	if !m.eng.AllowWrite() && !m.eng.AllowShell() {
		must = append(must, chip{th.Muted.Render("read only"), true})
	}
	if m.perm() == PermAsk {
		must = append(must, chip{th.Muted.Render("ask"), true})
	}
	// Select mode is a persistent interaction state: the wheel no longer
	// scrolls and the mouse belongs to the terminal. It must survive width
	// pressure (dropping it would look like a broken wheel), so it is a must
	// chip like the permission posture.
	if !m.mouseOn() {
		must = append(must, chip{th.Accent.Render(glyphSelect + " select"), true})
	}

	// Context: must when warn; else vanity with higher keep priority than tokens.
	limits := m.eng.Limits()
	var ctxChip *chip
	if limits.ContextWindow > 0 {
		if ct := m.eng.ContextTokens(); ct > 0 {
			label, level := formatContextPctLevel(ct, limits.ContextWindow)
			var cs lipgloss.Style
			switch level {
			case 2:
				cs = th.Warn
			case 1:
				cs = th.Accent.Bold(false)
			default:
				cs = th.Muted
			}
			text := cs.Render(label)
			// Gauge is additive and width-gated: only on roomy terminals, and
			// only alongside the number, which stays the source of truth. The
			// chip line is priority-dropped, so the bar must never be the
			// reason a safety chip loses its place.
			if m.width >= ctxGaugeMinWidth {
				if bar := m.ctxGauge(ct, limits.ContextWindow); bar != "" {
					text = bar + " " + text
				}
			}
			c := chip{text, level >= 2}
			ctxChip = &c
		}
	}
	if ctxChip != nil && ctxChip.must {
		must = append(must, *ctxChip)
	}

	// Vanity drop order (first dropped under width pressure):
	// focus → cost → tokens → goal → workspace; non-warn ctx kept longer.
	if m.focus == focusTranscript {
		vanity = append(vanity, chip{th.Accent.Render("focus:transcript"), false})
	}
	// Token activity observed this process (provider-reported; not a bill).
	// The total is host + peers so it answers "what did this run cost"; the
	// parenthesised peer share is called out because delegated spend is the
	// part users cannot see in the transcript. /status carries provenance and
	// the full input/output breakdown.
	if tok := m.tokIn + m.tokOut + m.peerTokIn + m.peerTokOut; tok > 0 {
		label := formatTokens(tok)
		if peer := m.peerTokIn + m.peerTokOut; peer > 0 {
			label += " (" + glyphPeer + " " + formatTokens(peer) + ")"
		}
		vanity = append(vanity, chip{th.Muted.Render(label + " tok"), false})
	}
	if gchip := goalHeaderChip(m.goalLive); gchip != "" {
		vanity = append(vanity, chip{th.Muted.Render(gchip), false})
	}
	if ctxChip != nil && !ctxChip.must {
		// Prefer keeping ctx over pure vanity: put at end (dropped last).
		vanity = append(vanity, chip{ctxChip.text, false})
	}

	ww := max(1, m.width)
	leftW := lipgloss.Width(left)
	budget := ww - leftW - 3 // leading space + min gap + trailing space
	if budget < 0 {
		budget = 0
	}

	join := func(cs []chip) string {
		if len(cs) == 0 {
			return ""
		}
		parts := make([]string, len(cs))
		for i, c := range cs {
			parts[i] = c.text
		}
		return strings.Join(parts, dot)
	}
	widthOf := func(cs []chip) int {
		if len(cs) == 0 {
			return 0
		}
		return lipgloss.Width(join(cs))
	}

	chosen := append([]chip{}, must...)
	keep := append([]chip{}, vanity...)
	for widthOf(append(chosen, keep...)) > budget && len(keep) > 0 {
		keep = keep[1:] // drop focus first, workspace/ctx last
	}
	right := join(append(chosen, keep...))
	gap := ww - leftW - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	if lipgloss.Width(line) > ww {
		right = join(must)
		gap = ww - leftW - lipgloss.Width(right) - 2
		if gap < 1 {
			gap = 1
		}
		line = " " + left + strings.Repeat(" ", gap) + right + " "
		if lipgloss.Width(line) > ww {
			line = xansi.Truncate(" "+left+" "+right+" ", ww, "")
		}
	}
	sep := m.theme.Sep.Render(strings.Repeat("─", ww))
	if lipgloss.Width(sep) > ww {
		sep = xansi.Truncate(sep, ww, "")
	}
	return line + "\n" + sep
}

// renderActivityBand is the one-row live-work strip above the input rule.
// Ephemeral: never written into the transcript. Idle → empty (zero height).
func (m *model) renderActivityBand() string {
	if !m.activityBandVisible() {
		return ""
	}
	th := m.theme
	ww := max(1, m.width)
	var left, right []string

	if m.busy {
		// Left owns "what is happening" + a fast ticker next to the spinner;
		// right (below) owns the per-turn elapsed and status counts.
		spin := m.spinnerView()
		// Phase ticker hugs the spinner: seconds since the last activity
		// (tool start/end, stream chunk, thinking). It resets whenever the
		// model moves, so the eye tracks the current phase, not the whole turn.
		since := m.lastActivityAt
		if since.IsZero() {
			since = m.startedAt
		}
		if since.IsZero() {
			left = append(left, spin)
		} else {
			left = append(left, spin+" "+th.Muted.Render(formatElapsed(time.Since(since))))
		}

		label := ""
		switch {
		case m.permWait != nil:
			label = th.Warn.Render("waiting")
		case m.peerActive.Load() > 1:
			// Parallel peers: the count lives on the right; suppress the single
			// last-writer tool label so it is not mistaken for the whole set.
			label = ""
		case m.toolCurrent == "writing":
			// Phase marker after tools/peers — the host is composing its answer.
			label = th.Muted.Render("composing")
		case m.toolCurrent != "":
			// Budget: yield exactly what the right-aligned status needs, not a
			// fixed reserve — with a short right (e.g. just "12s") the tool
			// label keeps the full remaining width (layoutBand still clamps).
			rightW := 0
			if !m.startedAt.IsZero() {
				rightW += lipgloss.Width(th.Muted.Render(formatElapsed(time.Since(m.startedAt))))
			}
			if n := int(m.peerActive.Load()); n > 1 {
				rightW += 6 + lipgloss.Width(th.Muted.Render(fmt.Sprintf("%d peers", n)))
			}
			if n := len(m.queued); n > 0 {
				rightW += 6 + lipgloss.Width(th.Muted.Render(fmt.Sprintf("queued · %d", n)))
			}
			used := lipgloss.Width(spin) + 16 // spinner + phase ticker
			maxLab := max(8, ww-used-rightW-8)
			// Peer labels (acp_delegate) keep a wider floor — the delegated
			// task detail is the information, so a squeezed band must not
			// ellipsize it away the way a generic verb label tolerates.
			if strings.Contains(m.toolCurrent, ":") {
				maxLab = max(maxLab, minPeerLabelWidth)
			}
			lab := activityToolLabel(m.toolCurrent, m.toolCurrentArgs, maxLab)
			if strings.HasPrefix(lab, glyphArrow) || strings.Contains(m.toolCurrent, ":") {
				label = th.Muted.Render(lab)
			} else {
				label = th.Muted.Render(glyphTool + " " + lab)
			}
		case m.reasonBuf != "" && m.streamBuf == "":
			label = th.Muted.Render("thinking")
		case m.streamBuf != "":
			label = th.Muted.Render("composing")
		default:
			label = th.Muted.Render("working")
		}
		if label != "" {
			left = append(left, label)
		}
		// Stall note: silence since last stream/tool event (not turn start).
		if !m.lastActivityAt.IsZero() {
			silent := time.Since(m.lastActivityAt)
			if silent >= 45*time.Second && m.toolCurrent == "" && m.streamBuf == "" {
				left = append(left, th.Muted.Faint(true).Render("no output · "+formatElapsed(silent)))
			}
		}

		// Right: per-turn elapsed since submit — the clock that matters for
		// long turns (30s → 5m → 20m as one request grinds on). Pinned to the
		// right edge so it does not jitter as the left label changes across
		// the busy heartbeat.
		if !m.startedAt.IsZero() {
			right = append(right, th.Muted.Render(formatElapsed(time.Since(m.startedAt))))
		}

		// peerAgent is last-writer-wins; with parallel peers show the count so
		// one agent/tool is not mistaken for the whole set.
		if n := int(m.peerActive.Load()); n > 1 {
			right = append(right, th.Muted.Render(fmt.Sprintf("%d peers", n)))
		}
	}

	if n := len(m.queued); n > 0 {
		right = append(right, th.Muted.Render(fmt.Sprintf("queued · %d", n)))
	}
	if m.permWait != nil && !m.busy {
		left = append(left, th.Warn.Render("waiting · permission"))
	}

	sepDot := th.Muted.Faint(true).Render("  " + glyphBullet + "  ")
	line := layoutBand(ww, left, right, sepDot)
	// One blank row above the content so the band breathes under the
	// transcript the same way the input rule separates the draft below.
	// layoutChrome counts this as activityBandRows.
	return strings.Repeat(" ", ww) + "\n" + line
}

// layoutBand lays the left parts at the left edge and the right parts
// right-aligned to ww, separated by at least gap spaces. The right group is
// pinned to the right edge (padding is derived from the live left width) so it
// stays at a fixed column even as the left label grows/shrinks each frame —
// no jitter during the busy heartbeat. Truncation-safe at narrow widths: the
// left label yields first (keeping the elapsed/status visible), and the
// assembled line is hard-clamped to ww so terminals never soft-wrap.
func layoutBand(ww int, left, right []string, sep string) string {
	const gap = 2
	ls := " " + strings.Join(left, sep)
	rs := strings.Join(right, sep) + " " // trailing inset, matches header
	lw := lipgloss.Width(ls)
	rw := lipgloss.Width(rs)
	// Pathologically narrow: clamp the right group, then the whole line.
	if rw > ww-gap-1 {
		rs = xansi.Truncate(rs, max(1, ww-gap-1), "…")
		rw = lipgloss.Width(rs)
	}
	// Left yields to keep the right-aligned status on screen.
	if maxAvail := ww - rw - gap; lw > maxAvail {
		ls = xansi.Truncate(ls, max(1, maxAvail), "…")
		lw = lipgloss.Width(ls)
	}
	pad := ww - lw - rw
	if pad < gap {
		pad = gap
	}
	line := ls + strings.Repeat(" ", pad) + rs
	if lipgloss.Width(line) > ww {
		line = xansi.Truncate(line, ww, "")
	}
	return line
}

func (m *model) renderInput() string {
	m.syncInputChrome()
	// Same language as the header: a single horizontal rule, not a box.
	// Exactly 1 + ta.Height() rows; clamp width to avoid soft-wrap chrome break.
	ww := max(1, m.width)
	sep := m.theme.Sep.Render(strings.Repeat("─", ww))
	if lipgloss.Width(sep) > ww {
		sep = xansi.Truncate(sep, ww, "")
	}
	// Soft horizontal inset, matching header's leading/trailing space.
	// Textarea normally wraps at its configured width, but a stale width during
	// resize/SetValue can leave one visual row wider than the viewport. Clamp
	// every rendered row here as the final frame-safety boundary.
	body := m.theme.Input.MaxWidth(ww).Width(ww).Render(m.ta.View())
	body = clampFrameLines(body, ww)
	return sep + "\n" + body
}

func clampFrameLines(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = xansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

// helpCard is the help modal body (no placement). Used as an overlay on mainFrame.
func (m *model) helpCard() string {
	k := m.cfg.Keys
	th := m.theme
	cardW := min(max(24, m.width-4), 60)
	// Inner width available to a row inside the box (border 2 + pad 2).
	inner := max(12, cardW-4)

	// keyCol is the fixed gutter for the key/command token; pad the *plain*
	// token before styling so ANSI never throws off the alignment.
	const keyCol = 14
	row := func(keyStyle lipgloss.Style, token, desc string) string {
		tok := short(token, keyCol)
		pad := keyCol - lipgloss.Width(tok)
		if pad < 1 {
			pad = 1
		}
		line := "  " + keyStyle.Render(tok) + strings.Repeat(" ", pad) + th.Muted.Render(desc)
		return short(line, inner)
	}
	section := func(title string) string {
		return "  " + th.Muted.Faint(true).Render(strings.ToUpper(title))
	}
	rule := th.Sep.Render(strings.Repeat("╌", inner))

	scroll := k.Primary(k.ScrollUp) + "/" + k.Primary(k.ScrollDown)
	var b strings.Builder
	// Header: wordmark + subtitle, then a divider.
	b.WriteString(" " + th.Accent.Render("mowi") + th.Muted.Render("  keys & commands") + "\n")
	b.WriteString(" " + rule + "\n\n")

	b.WriteString(section("keys") + "\n")
	for _, r := range []struct{ key, desc string }{
		{k.Primary(k.Send), "send  (queues while busy)"},
		{k.Primary(k.Newline), "newline  (input grows)"},
		{scroll, "scroll transcript  (wheel / ctrl+u / ctrl+d)"},
		{k.Primary(k.Focus), "focus editor ↔ transcript"},
		{k.Primary(k.SelectMode), "select mode (release mouse to copy)"},
		{k.Primary(k.PeerExpand), "peer output: collapsed ↔ live text"},
		{k.Primary(k.PermCycle), "perm  auto ↔ ask"},
		{k.Primary(k.Clear), "clear transcript"},
		{k.Primary(k.Cancel), "cancel turn · dismiss"},
		{k.Primary(k.Help), "help  (? when input empty)"},
		{k.Primary(k.Quit), "quit"},
	} {
		b.WriteString(row(th.Accent, r.key, r.desc) + "\n")
	}

	b.WriteString("\n" + section("commands") + "\n")
	for _, c := range []struct{ cmd, desc string }{
		{"/model", "picker · /model <id> to jump"},
		{"/goal", "list · status · new · run"},
		{"/lsp", "recent post-edit diagnostics"},
		{"/btw", "aside — not added to context"},
		{"/steer", "guide the running turn (while busy)"},
		{"/search", "find in transcript (repeat to cycle)"},
		{"/retry", "regenerate the last answer"},
		{"/edit", "edit last message  (or ↑ when empty)"},
		{"/sessions", "resume a session"},
		{"/copy", "yank last answer"},
		{"/status", "session details"},
		{"/perm", "auto | ask"},
	} {
		b.WriteString(row(th.SlashCmd, c.cmd, c.desc) + "\n")
	}
	// Pack-registered commands are listed from the registry rather than typed
	// out here, so help cannot claim a command that is not linked (or omit one
	// that is).
	for _, c := range slash.Commands() {
		b.WriteString(row(th.SlashCmd, "/"+c.Name, c.Summary) + "\n")
	}
	// Trivial commands share one quiet row (keeps /help /clear /quit discoverable).
	b.WriteString("  " + th.SlashCmd.Render("/help") + th.Muted.Render("   ") +
		th.SlashCmd.Render("/clear") + th.Muted.Render("   ") +
		th.SlashCmd.Render("/quit") + "\n")

	b.WriteString("\n" + section("permission") + "\n")
	b.WriteString(row(th.Accent, "y / n / a", "allow · deny · always") + "\n")
	b.WriteString(row(th.Muted, "header", "read only = safe · ▲ write / ▲ shell = those tools on") + "\n")

	b.WriteString("\n " + th.Muted.Faint(true).Render(short(k.Primary(k.Cancel)+" or ? to close", inner)))

	return th.Box.Width(cardW).Render(strings.TrimRight(b.String(), "\n"))
}

// inputHeightCap is the max textarea rows given terminal chrome.
// header(2) + input top rule(1) + min transcript(5) [+ perm strip].
func (m *model) inputHeightCap() int {
	capH := inputMaxHeight
	if m.height > 0 {
		room := m.height - 2 - 1 - 5
		if m.permWait != nil {
			room--
		}
		if room < inputMinHeight {
			room = inputMinHeight
		}
		if room < capH {
			capH = room
		}
	}
	return capH
}

// applyInputHeightCap sets DynamicHeight bounds from the terminal size.
func (m *model) applyInputHeightCap() {
	m.ta.DynamicHeight = true
	m.ta.MinHeight = inputMinHeight
	m.ta.MaxHeight = m.inputHeightCap()
}

// clampInputHeight enforces MaxHeight after a resize or cap change.
// Returns true when height was reduced (caller should re-layout).
func (m *model) clampInputHeight() bool {
	capH := m.inputHeightCap()
	m.ta.MaxHeight = capH
	if m.ta.Height() > capH {
		m.ta.SetHeight(capH)
		return true
	}
	return false
}

// syncInputHeight keeps DynamicHeight bounds in sync and re-applies height from
// content when needed (e.g. after SetValue outside Update). Returns true if
// Height() changed so the caller can re-layout chrome.
func (m *model) syncInputHeight() bool {
	before := m.ta.Height()
	m.applyInputHeightCap()
	// SetValue/Insert path: force a content-based height by re-setting value
	// only when DynamicHeight did not run (e.g. tests). Prefer re-measure via
	// a no-op InsertString of empty when value already set — SetValue works.
	v := m.ta.Value()
	// Re-trigger recalculateHeight inside bubbles (private) through SetValue.
	// Cursor moves to end — OK for tests and rare non-Update callers.
	m.ta.SetValue(v)
	m.clampInputHeight()
	return m.ta.Height() != before
}

// layoutChrome returns fixed chrome row counts: header, activity band, perm strip, input, total.
// Must match renderHeader / renderActivityBand / renderPermissionStrip / renderInput.
// activityBandRows is the chrome height of the activity band when visible:
// 1 blank top pad row + 1 content row.
const activityBandRows = 2

func (m *model) layoutChrome() (header, band, perm, input, chrome int) {
	header = 2 // title + rule
	if m.activityBandVisible() {
		band = activityBandRows
	}
	if m.permWait != nil {
		perm = 1
	}
	input = m.ta.Height() + 1 // rule + textarea
	chrome = header + band + perm + input
	return
}

func (m *model) activityBandVisible() bool {
	if m.permWait != nil {
		return true
	}
	if len(m.queued) > 0 {
		return true
	}
	if !m.busy {
		return false
	}
	// Busy: show band for spinner/elapsed/tool/thinking.
	return true
}

func (m *model) layout() {
	// Soft-wrapped transcript lines would desync chrome math (see roleBlock clamp).
	m.applyInputHeightCap()
	m.clampInputHeight()
	wantBand := m.activityBandVisible()
	if m.ready && wantBand != m.activityBandOn {
		m.compensateBandScroll(wantBand)
		m.activityBandOn = wantBand
	}
	_, _, _, _, chrome := m.layoutChrome()
	vh := m.height - chrome
	if vh < 3 && m.ta.Height() > inputMinHeight {
		m.ta.SetHeight(inputMinHeight)
		_, _, _, _, chrome = m.layoutChrome()
		vh = m.height - chrome
	}
	// Never force viewport taller than remaining space — tooSmall() covers
	// unusable terminals; keep vh ≥ 1 so the widget stays valid.
	if vh < 1 {
		vh = 1
	}
	w := max(1, m.width)
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(vh))
		m.vp.MouseWheelEnabled = true
		// Scroll keys from config; letter bindings stripped so typing never scrolls.
		up := m.cfg.Keys.All(m.cfg.Keys.ScrollUp)
		down := m.cfg.Keys.All(m.cfg.Keys.ScrollDown)
		if len(up) == 0 {
			up = []string{"ctrl+u"}
		}
		if len(down) == 0 {
			down = []string{"ctrl+d"}
		}
		m.vp.KeyMap = viewport.KeyMap{
			HalfPageUp:   key.NewBinding(key.WithKeys(up...), key.WithHelp(m.cfg.Keys.Primary(m.cfg.Keys.ScrollUp), "scroll up")),
			HalfPageDown: key.NewBinding(key.WithKeys(down...), key.WithHelp(m.cfg.Keys.Primary(m.cfg.Keys.ScrollDown), "scroll down")),
		}
		m.ready = true
	} else {
		m.vp.SetWidth(w)
		m.vp.SetHeight(vh)
	}
	// Width includes the textarea's horizontal padding, while textarea.SetWidth
	// receives the content frame width. Keep both in agreement so the first
	// prompt row does not exceed the visible terminal viewport.
	inputFrameW := max(1, m.width-2)
	m.ta.SetWidth(inputFrameW)
}
