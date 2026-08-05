package mowi

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// focusPane is which region receives scroll vs type keys.
type focusPane int

const (
	focusEditor focusPane = iota
	focusTranscript
)

// resizeSettleMsg fires after resize debounce to re-pretty assistants.
type resizeSettleMsg struct {
	width  int
	height int
	gen    uint64
}

const (
	resizeSettleDelay   = 120 * time.Millisecond
	historyLazyMaxPlain = 80 // beyond this, only last N get full async pretty
)

func (m *model) scheduleResizeSettle() tea.Cmd {
	m.resizeGen++
	gen := m.resizeGen
	w, h := m.width, m.height
	return tea.Tick(resizeSettleDelay, func(time.Time) tea.Msg {
		return resizeSettleMsg{width: w, height: h, gen: gen}
	})
}

func (m *model) handleResizeSettle(msg resizeSettleMsg) tea.Cmd {
	if msg.gen != m.resizeGen || msg.width != m.width || msg.height != m.height {
		return nil
	}
	// Async re-pretty recent assistants only (lazy history).
	w := max(24, m.vp.Width()-2)
	var cmds []tea.Cmd
	start := 0
	if len(m.entries) > historyLazyMaxPlain {
		start = len(m.entries) - historyLazyMaxPlain
	}
	for i := start; i < len(m.entries); i++ {
		e := &m.entries[i]
		if e.kind == kindAssistant && strings.TrimSpace(e.text) != "" {
			cmds = append(cmds, m.kickEntryPretty(i, e.text, w))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) toggleFocus() {
	if m.focus == focusEditor {
		m.focus = focusTranscript
	} else {
		m.focus = focusEditor
	}
}
