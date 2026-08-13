package mowi

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Keep full source text for the newest N entries; older user/assistant turns
// are stubbed so long sessions do not retain every answer in memory.
// Status/error/tool lines are cheap and kept as-is.
const (
	entryTextKeepFull  = 80 // full text retained for last N entries
	entryTextStubRunes = 96 // max runes in stub
)

func (m *model) gcOldEntryText() {
	// seedTranscript runs before the first WindowSize. Without a viewport,
	// every entry looks off-screen and a long resume would stub itself
	// into unreadable GC markers before the user can scroll.
	if !m.ready {
		return
	}
	n := len(m.entries)
	if n <= entryTextKeepFull {
		return
	}
	cutoff := n - entryTextKeepFull
	for i := 0; i < cutoff; i++ {
		e := &m.entries[i]
		if e.gc {
			continue
		}
		if e.kind != kindUser && e.kind != kindAssistant && e.kind != kindDiff {
			continue
		}
		// Never stub what the user is currently reading — a scrolled-up view
		// must not have its text replaced under the cursor. The next add()
		// after scrolling away collects it.
		if m.entryVisible(i) {
			continue
		}
		e.text = stubEntryText(e.kind, e.text)
		e.view = ""
		e.viewW = 0
		e.plain = true
		e.gc = true
		if m.prettyWant != nil {
			delete(m.prettyWant, i)
		}
	}
}

// entryVisible reports whether entry i intersects the current viewport band
// (best-effort from the last virtual build; unknown index = visible = keep).
func (m *model) entryVisible(i int) bool {
	if !m.ready {
		return false
	}
	if m.followBottom {
		// Pinned to bottom: old (GC-eligible) entries are far above the view.
		return false
	}
	if i >= len(m.entryLineStart) || i >= len(m.entryHeights) {
		return true
	}
	y := m.vp.YOffset()
	vh := m.vp.Height()
	if vh < 1 {
		vh = 1
	}
	start := m.entryLineStart[i]
	h := m.entryHeights[i]
	if h < 1 {
		h = 1
	}
	return start+h > y-keepViewRadius && start < y+vh+keepViewRadius
}

func stubEntryText(kind entryKind, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "…(gc)"
	}
	// Prefer first non-empty line as summary.
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = strings.TrimSpace(text[:i])
	}
	if utf8.RuneCountInString(first) > entryTextStubRunes {
		r := []rune(first)
		first = string(r[:entryTextStubRunes]) + "…"
	}
	label := "msg"
	switch kind {
	case kindUser:
		label = "you"
	case kindAssistant:
		label = "mowi"
	case kindDiff:
		label = "diff"
	}
	return fmt.Sprintf("…(%s gc) %s", label, first)
}
