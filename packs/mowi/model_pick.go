package mowi

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/subosito/mow"
)

// modelPicker is an interactive /model overlay (↑↓/j/k, enter, esc).
type modelPicker struct {
	items   []mow.ModelInfo
	idx     int
	filter  string
	current string
}

// normalizeModelFilter trims spaces and a trailing " [wire]" display suffix
// so pasting a catalog line still matches the id.
func normalizeModelFilter(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip "  [openai-responses]" style annotations from list display.
	if i := strings.LastIndex(s, "["); i > 0 && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[i+1 : len(s)-1])
		// Only strip when it looks like a wire id (has a hyphen or known prefix).
		if strings.Contains(inner, "-") || strings.HasPrefix(inner, "openai") || strings.HasPrefix(inner, "anthropic") {
			s = strings.TrimSpace(s[:i])
		}
	}
	return s
}

// openModelPicker focuses the list with current model selected when present.
func (m *model) openModelPicker(items []mow.ModelInfo, current, filter string) {
	if len(items) == 0 {
		m.modelPick = nil
		return
	}
	idx := 0
	for i, info := range items {
		if current != "" && strings.EqualFold(info.ID, current) {
			idx = i
			break
		}
	}
	m.modelPick = &modelPicker{
		items:   items,
		idx:     idx,
		filter:  filter,
		current: current,
	}
}

func (m *model) closeModelPicker() {
	m.modelPick = nil
}

// handleModelPickKey routes keys while the model picker is open.
func (m *model) handleModelPickKey(keyStr string, msg tea.KeyPressMsg) tea.Cmd {
	p := m.modelPick
	if p == nil || len(p.items) == 0 {
		m.closeModelPicker()
		return nil
	}
	ks := m.cfg.Keys
	switch {
	case ks.Matches(ks.Cancel, keyStr) || msg.Code == tea.KeyEsc || keyStr == "q" || keyStr == "esc":
		m.closeModelPicker()
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
		info := p.items[p.idx]
		m.closeModelPicker()
		return m.applyModelInfo(info)
	default:
		// Ignore typing / other keys while picker is open.
		return nil
	}
}

// applyModelInfo switches model (and catalog wire when present) off the UI thread.
func (m *model) applyModelInfo(info mow.ModelInfo) tea.Cmd {
	eng := m.eng
	id := strings.TrimSpace(info.ID)
	wire := strings.TrimSpace(info.Wire)
	return func() tea.Msg {
		if eng == nil {
			return modelListMsg{err: fmt.Errorf("no engine")}
		}
		// Catalog wire when known; empty wire keeps the current/default wire.
		if err := eng.SetModelWithWire(id, wire); err != nil {
			return modelListMsg{current: eng.Model(), err: err}
		}
		return modelListMsg{
			setTo:   id,
			setWire: eng.Wire(), // effective wire after apply
			current: id,
		}
	}
}

// modelPickerCard renders the picker body (no placement).
func (m *model) modelPickerCard() string {
	p := m.modelPick
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
		// Keep selection in the window.
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
	title := "models"
	if p.filter != "" {
		title = fmt.Sprintf("models matching %q", p.filter)
	}
	b.WriteString(" " + th.Accent.Render(title))
	if p.current != "" {
		b.WriteString(th.Muted.Render("  current " + p.current))
	}
	if w := m.eng.Wire(); w != "" {
		b.WriteString(th.Muted.Render("  · wire " + w))
	}
	b.WriteString("\n")
	rule := th.Sep.Render(strings.Repeat("╌", inner))
	b.WriteString(" " + rule + "\n")

	for i := start; i < end; i++ {
		info := p.items[i]
		mark := "  "
		style := th.Muted
		if i == p.idx {
			mark = "▸ "
			style = th.Accent
		} else if p.current != "" && strings.EqualFold(info.ID, p.current) {
			mark = "• "
			style = th.Text
		}
		line := mark + info.ID
		if info.Wire != "" {
			line += "  " + th.Muted.Render("["+info.Wire+"]")
			// Re-style: accent for selected already applied to id only is hard
			// with mixed styles — rebuild selected line fully.
			if i == p.idx {
				line = mark + info.ID
				if info.Wire != "" {
					line += "  [" + info.Wire + "]"
				}
				line = style.Render(line)
			} else {
				line = mark + style.Render(info.ID)
				if info.Wire != "" {
					line += "  " + th.Muted.Render("["+info.Wire+"]")
				}
			}
		} else {
			line = mark + style.Render(info.ID)
		}
		b.WriteString(" " + short(line, inner) + "\n")
	}
	if start > 0 || end < n {
		b.WriteString(" " + th.Muted.Faint(true).Render(fmt.Sprintf("  %d–%d of %d", start+1, end, n)) + "\n")
	}
	b.WriteString("\n " + th.Muted.Faint(true).Render(short("↑↓/j k  enter select  esc cancel", inner)))

	return th.Box.Width(cardW).Render(strings.TrimRight(b.String(), "\n"))
}

// cmdModel lists or switches models (async).
//
//	/model           → open interactive picker
//	/model <id>      → switch if exact/unique match, else filtered picker
func (m *model) cmdModel(filter string) tea.Cmd {
	eng := m.eng
	filter = normalizeModelFilter(filter)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		list, err := eng.ListModels(ctx)
		cur := eng.Model()
		if err != nil {
			return modelListMsg{current: cur, filter: filter, err: err}
		}
		// Chat UI: same filter as mow REPL / ACP (facet chat|empty, known chat wire).
		list = mow.FilterChatModels(list)
		if filter == "" {
			return modelListMsg{models: list, current: cur, openPicker: true}
		}
		apply := func(info mow.ModelInfo) modelListMsg {
			if err := eng.SetModelWithWire(info.ID, info.Wire); err != nil {
				return modelListMsg{current: cur, err: err}
			}
			// Report effective wire (catalog wire or previous default).
			return modelListMsg{setTo: info.ID, setWire: eng.Wire(), current: info.ID}
		}
		for _, info := range list {
			if strings.EqualFold(info.ID, filter) {
				return apply(info)
			}
		}
		var matched []mow.ModelInfo
		fl := strings.ToLower(filter)
		for _, info := range list {
			if strings.Contains(strings.ToLower(info.ID), fl) {
				matched = append(matched, info)
			}
		}
		if len(matched) == 1 {
			return apply(matched[0])
		}
		if len(matched) == 0 {
			if len(list) > 0 {
				return modelListMsg{
					current:    cur,
					filter:     filter,
					models:     list,
					openPicker: true,
					err:        fmt.Errorf("no catalog model matching %q", filter),
				}
			}
			// No catalog — force set id, keep default wire.
			if err := eng.SetModel(filter); err != nil {
				return modelListMsg{current: cur, filter: filter, err: err}
			}
			return modelListMsg{setTo: filter, setWire: eng.Wire(), current: filter}
		}
		return modelListMsg{models: matched, current: cur, filter: filter, openPicker: true}
	}
}
