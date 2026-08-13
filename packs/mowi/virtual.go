package mowi

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// Virtualized transcript: off-screen entries keep source text but drop ANSI
// view caches and contribute only blank line placeholders to the viewport
// document (correct total height, minimal memory for styled content).

const (
	// prettyWindow: recent entries always fully rendered (glamour when assistant).
	prettyWindow = 48
	// scrollPrettyRadius: entries near scroll top upgraded to full render.
	scrollPrettyRadius = 16
	// keepViewRadius: clear .view outside this margin of the viewport.
	keepViewRadius = 24
)

func (m *model) ensureHistoryCacheVirtual(w int) {
	if m.historyCacheW == w && m.historyCacheN == len(m.entries) && m.historyCacheN >= 0 && !m.historyDirty {
		return
	}
	// Heights without full ANSI for far entries.
	m.ensureEntryHeights(w)

	// Visible window in line space (best-effort from last YOffset / follow).
	vh := m.vp.Height()
	if vh < 1 {
		vh = 1
	}
	total := m.totalEntryLines()
	y := 0
	if m.ready {
		if m.followBottom {
			y = max(0, total-vh)
		} else {
			y = m.vp.YOffset()
		}
	}
	visLo, visHi := y-keepViewRadius, y+vh+keepViewRadius
	if visLo < 0 {
		visLo = 0
	}

	var b strings.Builder
	m.entryLineStart = m.entryLineStart[:0]
	line := 0
	n := len(m.entries)
	for i := 0; i < n; i++ {
		if i > 0 {
			// Inter-entry air. Entries never end in a newline, so
			// strings.Repeat of newlines terminates the previous line and
			// adds `blanks` blank lines (T2: stronger gap before user turns).
			blanks := entrySepBefore(m.entries[i])
			b.WriteString(strings.Repeat("\n", 1+blanks))
			line += blanks
		}
		m.entryLineStart = append(m.entryLineStart, line)
		h := m.entryHeights[i]
		if h < 1 {
			h = 1
		}
		entryEnd := line + h
		// Intersects visible band?
		onScreen := entryEnd > visLo && line < visHi
		nearEnd := i >= n-prettyWindow
		force := nearEnd || (m.prettyWant != nil && m.prettyWant[i])

		if onScreen || force {
			// Normalize: no trailing newline, so every entry occupies exactly
			// its line count and the "\n\n" separator is always one blank
			// line. Mixed trailing newlines drifted entryLineStart by +1 per
			// bare-line entry (tool/status), skewing the visible-band test.
			view := strings.TrimRight(m.entryViewVirtual(i, w, force), "\n")
			b.WriteString(view)
			actual := strings.Count(view, "\n") + 1
			line = m.entryLineStart[i] + actual
			m.entryHeights[i] = actual
		} else {
			// Off-screen: drop cached ANSI; emit blank lines of estimated height.
			e := &m.entries[i]
			e.view = ""
			e.viewW = 0
			e.plain = true
			// Placeholder newlines (height-1 extra after first empty line content).
			// One visual line = "\n" between rows; height lines need height segments.
			for j := 0; j < h; j++ {
				if j > 0 {
					b.WriteByte('\n')
				}
				// single space keeps line non-empty for some renderers
				b.WriteByte(' ')
			}
			line += h
		}
	}
	m.historyCache = b.String()
	m.historyCacheW = w
	m.historyCacheN = len(m.entries)
	m.historyDirty = false
}

func countContentLines(s string) int {
	if s == "" {
		return 1
	}
	n := strings.Count(s, "\n") + 1
	if strings.HasSuffix(s, "\n") {
		n = strings.Count(s, "\n")
	}
	if n < 1 {
		return 1
	}
	return n
}

func (m *model) ensureEntryHeights(w int) {
	if cap(m.entryHeights) < len(m.entries) {
		m.entryHeights = make([]int, len(m.entries))
	} else {
		m.entryHeights = m.entryHeights[:len(m.entries)]
	}
	inner := max(16, w-roleGutterW)
	for i := range m.entries {
		e := &m.entries[i]
		if e.view != "" && e.viewW == w {
			m.entryHeights[i] = countContentLines(e.view)
			continue
		}
		wrap := inner
		if e.kind == kindUser {
			wrap = max(12, inner-2) // RoleUserBg horizontal pad
		}
		m.entryHeights[i] = estimateEntryHeight(e, wrap)
	}
}

// entrySepLines is default blank-line air between related entries
// (assistant/tool/status glue). entrySepTurnLines is used before a new user
// turn for slightly stronger document rhythm (T2) without chat bubbles.
const (
	entrySepLines     = 1
	entrySepTurnLines = 2
)

// entrySepBefore returns blank lines before e (not counting the newline that
// terminates the previous entry's last content line).
func entrySepBefore(e entry) int {
	if e.kind == kindUser {
		return entrySepTurnLines
	}
	return entrySepLines
}

func (m *model) totalEntryLines() int {
	if len(m.entryHeights) == 0 {
		return 0
	}
	total := 0
	for i, h := range m.entryHeights {
		if i > 0 {
			total += entrySepBefore(m.entries[i])
		}
		if h < 1 {
			h = 1
		}
		total += h
	}
	return total
}

func estimateEntryHeight(e *entry, inner int) int {
	text := e.text
	if text == "" {
		return 1
	}
	// roleBlock adds gutter; word-wrap on inner.
	wrapped := wordWrap(text, inner)
	n := countContentLines(wrapped)
	// Timestamps are inline (user block first line) — no extra row.
	// status/error may be single styled line
	if e.kind == kindTool || e.kind == kindStatus || e.kind == kindError {
		// often one line unless long
		if xansi.StringWidth(text) <= inner+8 {
			return 1
		}
	}
	return n
}

// entryViewVirtual returns a render for entry i.
func (m *model) entryViewVirtual(i, width int, forcePretty bool) string {
	e := &m.entries[i]
	if e.view != "" && e.viewW == width && (!e.plain || !forcePretty) {
		return e.view
	}
	// GC'd entries cannot be re-glamoured; always plain stub.
	if e.gc {
		inner := max(16, width-roleGutterW)
		if e.kind == kindUser {
			inner = max(12, inner-2)
		}
		e.view = m.renderTurn(e.kind == kindUser, wordWrap(e.text, inner), e.at, width)
		e.viewW = width
		e.plain = true
		return e.view
	}
	if e.kind == kindAssistant && !forcePretty && !m.shouldPretty(i) {
		inner := max(16, width-roleGutterW)
		e.view = m.renderTurn(false, wordWrap(e.text, inner), e.at, width)
		e.viewW = width
		e.plain = true
		return e.view
	}
	e.view = m.renderEntry(*e, width)
	e.viewW = width
	e.plain = false
	return e.view
}

func (m *model) shouldPretty(i int) bool {
	n := len(m.entries)
	if n == 0 {
		return true
	}
	if i >= n-prettyWindow {
		return true
	}
	return m.prettyWant != nil && m.prettyWant[i]
}

// afterScrollPretty upgrades plain assistants near the viewport and rebuilds.
func (m *model) afterScrollPretty() tea.Cmd {
	if len(m.entries) == 0 || !m.ready {
		return nil
	}
	y := m.vp.YOffset()
	idx := 0
	for i, start := range m.entryLineStart {
		if start <= y {
			idx = i
		}
	}
	lo := max(0, idx-scrollPrettyRadius)
	hi := min(len(m.entries), idx+scrollPrettyRadius+1)
	w := max(24, m.vp.Width()-2)
	if m.prettyWant == nil {
		m.prettyWant = map[int]bool{}
	}
	var cmds []tea.Cmd
	for i := lo; i < hi; i++ {
		e := &m.entries[i]
		if e.gc || e.kind != kindAssistant || strings.TrimSpace(e.text) == "" {
			continue
		}
		if !e.plain && e.view != "" && e.viewW == w {
			continue
		}
		m.prettyWant[i] = true
		e.view = ""
		e.viewW = 0
		e.plain = false
		cmds = append(cmds, m.kickEntryPretty(i, e.text, w))
	}
	// Drop view caches far from viewport to free ANSI bytes.
	for i := range m.entries {
		if i >= lo && i < hi {
			continue
		}
		if i >= len(m.entries)-prettyWindow {
			continue
		}
		e := &m.entries[i]
		if e.view != "" {
			e.view = ""
			e.viewW = 0
			e.plain = true
		}
	}
	// Always rebuild now: off-screen rows are blank placeholders, and a
	// resumed session starts pinned to the bottom. Waiting for pretty jobs
	// would leave the newly visible band empty until they land.
	m.historyDirty = true
	m.applyVPContent()
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}
