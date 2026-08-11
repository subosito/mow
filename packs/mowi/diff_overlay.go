package mowi

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// diffOverlay is the expanded full-screen diff viewer.
//
// The compact transcript card stays the default; this opens on demand so a
// large or multi-hunk change can be read without scrolling the whole session.
// Dismiss restores the transcript viewport offset saved at open.
type diffOverlay struct {
	op, path, body string
	mode           diffViewMode
	vp             viewport.Model
	// Restore transcript position on dismiss.
	savedYOffset int
	savedFollow  bool
	// contentW/H cache the last painted geometry so resize can rebuild.
	contentW, contentH int
}

// openDiffOverlay opens the last kindDiff entry (or entry at idx if >= 0).
// Returns false when there is nothing to show.
func (m *model) openDiffOverlay(idx int) bool {
	if idx < 0 {
		idx = m.lastDiffEntryIndex()
	}
	if idx < 0 || idx >= len(m.entries) {
		return false
	}
	e := m.entries[idx]
	if e.kind != kindDiff || strings.TrimSpace(e.text) == "" {
		return false
	}
	op, path, body := parseDiffEntry(e.text)
	if body == "" && path == "" {
		return false
	}
	// Prefer path from ---/+++ when the title path is empty.
	if path == "" {
		path = parseUnifiedDiff(body).Path
	}
	o := &diffOverlay{
		op:           op,
		path:         path,
		body:         body,
		mode:         diffModeUnified,
		savedYOffset: m.vp.YOffset(),
		savedFollow:  m.followBottom,
	}
	o.vp = viewport.New(viewport.WithWidth(max(1, m.width)), viewport.WithHeight(max(1, m.height-2)))
	m.diffView = o
	m.layoutDiffOverlay()
	return true
}

// lastDiffEntryIndex returns the newest kindDiff entry, or -1.
func (m *model) lastDiffEntryIndex() int {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == kindDiff && strings.TrimSpace(m.entries[i].text) != "" {
			return i
		}
	}
	return -1
}

// closeDiffOverlay dismisses the overlay and restores transcript scroll.
func (m *model) closeDiffOverlay() {
	if m.diffView == nil {
		return
	}
	y := m.diffView.savedYOffset
	follow := m.diffView.savedFollow
	m.diffView = nil
	m.layout()
	m.refreshVP()
	// Restore position after layout/refresh may have re-pinned to bottom.
	if follow {
		m.followBottom = true
		m.vp.GotoBottom()
	} else {
		m.followBottom = false
		m.vp.SetYOffset(y)
	}
}

// layoutDiffOverlay sizes the overlay viewport and repaints its content.
func (m *model) layoutDiffOverlay() {
	o := m.diffView
	if o == nil {
		return
	}
	w := max(1, m.width)
	// Title (1) + rule (1) + body.
	h := max(1, m.height-2)
	o.vp.SetWidth(w)
	o.vp.SetHeight(h)
	o.contentW, o.contentH = w, h
	o.vp.SetContent(m.renderDiffOverlayBody())
}

// renderDiffOverlayBody paints the full (uncollapsed) diff in the current mode.
func (m *model) renderDiffOverlayBody() string {
	o := m.diffView
	if o == nil {
		return ""
	}
	w := max(24, o.contentW)
	mode := o.mode
	if mode == diffModeSplit && !splitModeAvailable(w) {
		mode = diffModeUnified
	}
	d := parseUnifiedDiff(o.body)
	if o.path != "" {
		d.Path = o.path
	}
	return renderDiffModel(m.theme, d, diffPaintOpts{
		Path:   d.Path,
		Mode:   mode,
		Width:  w,
		Syntax: true,
	})
}

// renderDiffOverlayFrame is the full-screen view: title + rule + body.
func (m *model) renderDiffOverlayFrame() string {
	o := m.diffView
	if o == nil {
		return ""
	}
	w := max(1, m.width)
	add, del := countDiffStats(o.body)
	canSplit := splitModeAvailable(w)
	// If split was requested but width shrank, paint as unified without
	// silently clearing the user's mode preference (it returns when wide).
	title := formatDiffOverlayTitle(m.theme, o.op, o.path, add, del, o.mode, w, canSplit)
	if lipgloss.Width(title) > w {
		title = xansi.Truncate(title, w, "")
	}
	rule := m.theme.Sep.Render(strings.Repeat("─", w))
	body := o.vp.View()
	return title + "\n" + rule + "\n" + body
}

// handleDiffOverlayKey routes keys while the overlay is open.
// Returns (handled, cmd).
func (m *model) handleDiffOverlayKey(keyStr string, msg tea.KeyPressMsg) (bool, tea.Cmd) {
	o := m.diffView
	if o == nil {
		return false, nil
	}
	ks := m.cfg.Keys
	switch {
	case ks.Matches(ks.Cancel, keyStr) || keyStr == "q":
		m.closeDiffOverlay()
		return true, nil
	case ks.Matches(ks.Quit, keyStr):
		// Quit while overlay is open: close overlay only (idle quit is a
		// second press). Matches help's non-busy dismiss behaviour.
		m.closeDiffOverlay()
		return true, nil
	case keyStr == "tab" || keyStr == "s":
		// Toggle unified ↔ split when width allows; otherwise stay unified
		// and leave a quiet status if the user asked for split at a narrow size.
		if o.mode == diffModeUnified {
			if splitModeAvailable(max(1, m.width)) {
				o.mode = diffModeSplit
			}
		} else {
			o.mode = diffModeUnified
		}
		// Rebuild content; keep scroll percent roughly stable.
		pct := o.vp.ScrollPercent()
		m.layoutDiffOverlay()
		if pct > 0 && o.vp.TotalLineCount() > o.vp.VisibleLineCount() {
			// Approximate restore via YOffset.
			maxOff := max(0, o.vp.TotalLineCount()-o.vp.VisibleLineCount())
			o.vp.SetYOffset(int(pct * float64(maxOff)))
		}
		return true, nil
	case ks.Matches(ks.ScrollUp, keyStr):
		o.vp.HalfPageUp()
		return true, nil
	case ks.Matches(ks.ScrollDown, keyStr):
		o.vp.HalfPageDown()
		return true, nil
	case keyStr == "up" || keyStr == "k":
		o.vp.ScrollUp(1)
		return true, nil
	case keyStr == "down" || keyStr == "j":
		o.vp.ScrollDown(1)
		return true, nil
	case keyStr == "pgup" || keyStr == "pgdown" || keyStr == "home" || keyStr == "end":
		var cmd tea.Cmd
		o.vp, cmd = o.vp.Update(msg)
		return true, cmd
	case ks.Matches(ks.ViewDiff, keyStr):
		// Second press closes (toggle).
		m.closeDiffOverlay()
		return true, nil
	default:
		// Swallow other keys so they do not hit the input or global bindings.
		return true, nil
	}
}

// toggleDiffOverlay opens the last diff or closes if already open.
func (m *model) toggleDiffOverlay() bool {
	if m.diffView != nil {
		m.closeDiffOverlay()
		return true
	}
	return m.openDiffOverlay(-1)
}
