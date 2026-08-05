package mowi

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// effortPicker is an interactive /effort overlay (↑↓/j/k, enter, esc).
type effortPicker struct {
	items   []string
	idx     int
	current string
}

// openEffortPicker shows the effort selector.
func (m *model) openEffortPicker() {
	efforts := m.eng.Efforts()
	if len(efforts) == 0 {
		efforts = []string{"none", "low", "medium", "high"}
	}
	cur := m.eng.Effort()
	idx := 0
	for i, e := range efforts {
		if strings.EqualFold(e, cur) {
			idx = i
			break
		}
	}
	m.effortPick = &effortPicker{
		items:   efforts,
		idx:     idx,
		current: cur,
	}
}

func (m *model) closeEffortPicker() {
	m.effortPick = nil
}

// handleEffortPickKey routes keys while the effort picker is open.
func (m *model) handleEffortPickKey(keyStr string, msg tea.KeyPressMsg) tea.Cmd {
	p := m.effortPick
	if p == nil || len(p.items) == 0 {
		m.closeEffortPicker()
		return nil
	}
	ks := m.cfg.Keys
	switch {
	case ks.Matches(ks.Cancel, keyStr) || msg.Code == tea.KeyEsc || keyStr == "q" || keyStr == "esc":
		m.closeEffortPicker()
		return nil
	case msg.Code == tea.KeyUp || keyStr == "k" || keyStr == "ctrl+p":
		if p.idx > 0 {
			p.idx--
		}
		return nil
	case msg.Code == tea.KeyDown || keyStr == "j" || keyStr == "ctrl+n":
		if p.idx < len(p.items)-1 {
			p.idx++
		}
		return nil
	case msg.Code == tea.KeyHome || keyStr == "g":
		p.idx = 0
		return nil
	case msg.Code == tea.KeyEnd || keyStr == "G":
		p.idx = len(p.items) - 1
		return nil
	case ks.Matches(ks.Send, keyStr) || keyStr == "enter":
		selected := p.items[p.idx]
		m.closeEffortPicker()
		if err := m.eng.SetEffort(selected); err != nil {
			m.add(kindError, fmt.Sprintf("effort: %v", err))
			return nil
		}
		m.add(kindStatus, "effort → "+selected)
		m.layout()
		return nil
	default:
		return nil
	}
}

// effortPickerCard renders the picker body (no placement).
func (m *model) effortPickerCard() string {
	p := m.effortPick
	if p == nil {
		return ""
	}
	th := m.theme
	cardW := min(max(28, m.width-4), 72)
	inner := max(16, cardW-4)

	const maxVisible = 12
	n := len(p.items)
	start := 0
	if n > maxVisible {
		start = p.idx - maxVisible/2
		if start < 0 {
			start = 0
		}
		if start+maxVisible > n {
			start = n - maxVisible
		}
	}
	end := start + maxVisible
	if end > n {
		end = n
	}

	var b strings.Builder
	title := "effort"
	b.WriteString(" " + th.Accent.Render(title))
	if p.current != "" {
		b.WriteString(th.Muted.Render("  current " + p.current))
	}
	b.WriteString("\n")
	rule := th.Sep.Render(strings.Repeat("╌", inner))
	b.WriteString(" " + rule + "\n")

	for i := start; i < end; i++ {
		e := p.items[i]
		mark := "  "
		style := th.Muted
		if i == p.idx {
			mark = "▸ "
			style = th.Accent
		} else if p.current != "" && strings.EqualFold(e, p.current) {
			mark = "• "
			style = th.Text
		}
		line := mark + style.Render(e)
		b.WriteString(" " + short(line, inner) + "\n")
	}
	if start > 0 || end < n {
		b.WriteString(" " + th.Muted.Faint(true).Render(fmt.Sprintf("  %d–%d of %d", start+1, end, n)) + "\n")
	}
	b.WriteString("\n " + th.Muted.Faint(true).Render(short("↑↓/j k  enter select  esc cancel", inner)))

	return th.Box.Width(cardW).Render(strings.TrimRight(b.String(), "\n"))
}
