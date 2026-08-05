package mowi

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func peerKey(agent string) string {
	agent = strings.TrimSpace(strings.ToLower(agent))
	if agent == "" {
		return "peer"
	}
	return agent
}

func (m *model) ensurePeerBuf(agent string) *peerLiveBuf {
	key := peerKey(agent)
	if m.peerBufs == nil {
		m.peerBufs = map[string]*peerLiveBuf{}
	}
	if b, ok := m.peerBufs[key]; ok {
		return b
	}
	name := strings.TrimSpace(agent)
	if name == "" {
		name = "peer"
	}
	b := &peerLiveBuf{agent: name}
	m.peerBufs[key] = b
	m.peerOrder = append(m.peerOrder, key)
	return b
}

// maxPeerBufBytes caps each peer's live buffer. A long reasoning stream would
// otherwise grow the buffer without bound while the body re-renders every
// frame; trimming the head keeps the live area bounded (the committed reply
// is unaffected).
const maxPeerBufBytes = 8 << 10

// trimUTF8Tail keeps the newest maxBytes without splitting a UTF-8 rune.
func trimUTF8Tail(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && (s[start]&0xc0) == 0x80 {
		start++
	}
	return s[start:]
}

func (m *model) appendPeerDelta(agent, delta string) {
	if delta == "" {
		return
	}
	b := m.ensurePeerBuf(agent)
	safe := sanitizeDisplay(delta)
	// Keep the complete answer separately: buf is intentionally bounded for
	// live rendering, but finishPeerStream must never commit a tail-truncated
	// or mid-markdown reply. A full reply is still bounded by the engine/tool
	// result policy; this TUI buffer only avoids corrupting the beginning.
	b.full += safe
	b.buf += safe
	if len(b.buf) > maxPeerBufBytes {
		// Keep the tail (most recent) for the live area and reset the body so the next render
		// rebuilds from scratch rather than carrying a stale prefix.
		b.buf = trimUTF8Tail(b.buf, maxPeerBufBytes)
		b.body, b.bodySrc = "", ""
	}
	b.dirty = true
	m.lastActivityAt = time.Now()
}

func (m *model) clearPeerBufs() {
	m.peerBufs = nil
	m.peerOrder = nil
}

// peerLiveCollapsed reports whether peer streams paint as a one-line summary
// instead of full live text. Collapsed is the default: a delegated agent's
// reasoning is secondary to the host transcript, and repainting a multi-line
// body on every delta is what makes the screen flicker and fights terminal
// text selection. The full reply is still committed when the peer finishes,
// so nothing is lost by not watching it stream. Toggle live with ctrl+p.
func (m *model) peerLiveCollapsed() bool {
	return !m.peerExpanded
}

// peerLiveSummaries renders one compact line per active peer:
//
//	→ grok   ⋯ 1.2k chars · thinking
//
// Cost is O(peers) per frame instead of O(text), so a long reasoning stream
// no longer redraws the transcript region on every chunk.
func (m *model) peerLiveSummaries() string {
	if len(m.peerOrder) == 0 {
		return ""
	}
	labelW := 0
	for _, key := range m.peerOrder {
		if b := m.peerBufs[key]; b != nil {
			labelW = max(labelW, peerLabelWidth(b.agent))
		}
	}
	var parts []string
	for _, key := range m.peerOrder {
		b := m.peerBufs[key]
		if b == nil {
			continue
		}
		label := m.rolePrefix(false) + m.peerLabel(b.agent, labelW)
		note := m.theme.Muted.Faint(true).Render(
			" " + glyphMore + " " + humanCount(len(b.buf)) + " · streaming",
		)
		parts = append(parts, label+note)
	}
	return strings.Join(parts, "\n")
}

// humanCount renders a byte count compactly (1234 -> "1.2k") so the peer
// summary line stays stable in width as the stream grows.
func humanCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d chars", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1fk chars", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM chars", float64(n)/(1000*1000))
	}
}

// peerModeLabel names the current peer display mode for status lines.
func peerModeLabel(expanded bool) string {
	if expanded {
		return "live text"
	}
	return "collapsed (one line per peer)"
}

// invalidateStreamFrame forces the next layout to rebuild the live frame.
// The cached frame is keyed on width, so a mode change with an unchanged
// width would otherwise keep painting the previous shape.
func (m *model) invalidateStreamFrame() {
	m.streamFrame = ""
	m.streamFrameW = -1
}

func (m *model) peerLiveBodies(inner int) string {
	if len(m.peerOrder) == 0 {
		return ""
	}
	if m.peerLiveCollapsed() {
		return m.peerLiveSummaries()
	}
	// Align every peer's label to the widest name so short ("peer-a") and long
	// ("claude-sonnet-4") agents share one body column instead of ragged indents.
	labelW := 0
	for _, key := range m.peerOrder {
		if b := m.peerBufs[key]; b != nil {
			labelW = max(labelW, peerLabelWidth(b.agent))
		}
	}
	var parts []string
	caret := m.theme.Muted.Faint(true).Render(" " + glyphCaret)
	for _, key := range m.peerOrder {
		b := m.peerBufs[key]
		if b == nil {
			continue
		}
		body := peerLiveBody(b, inner)
		// Plain fallback is temporary while the async markdown render catches
		// up. The next streamRenderedMsg replaces it with the faint MD body.
		if strings.IndexByte(body, 0x1b) < 0 && body != "" {
			body = m.theme.Muted.Faint(true).Render(body)
		}
		if body == "" {
			body = caret
		} else {
			body += caret
		}
		// Indent the label into the role gutter so it lines up with its own
		// body (roleBlock prefixes every line) and the separator rule below —
		// otherwise the "→ agent" tag hangs flush-left off its indented block.
		label := m.rolePrefix(false) + m.peerLabel(b.agent, labelW)
		parts = append(parts, label+"\n"+m.roleBlock(false, body))
	}
	return strings.Join(parts, "\n"+m.peerSepRule(inner)+"\n")
}

// peerLabelWidth is the terminal cell width of a peer's "→ name" label, used
// to size the shared gutter. The arrow glyph is double-wide in many fonts, so
// measure the rendered string rather than counting runes.
func peerLabelWidth(agent string) int {
	return xansi.StringWidth(glyphArrow + " " + agent)
}

// peerLabel renders a peer's "→ name" tag, right-padded to width so every
// peer's body starts in the same column. The arrow + alignment pad stay faint
// chrome (peers are in-flight secondary text), but the agent NAME pops in
// accent+bold — one accent element per line — so "who is speaking" is visible
// at a glance while the body below keeps its intentional low-priority dim.
func (m *model) peerLabel(agent string, width int) string {
	raw := glyphArrow + " " + agent
	pad := ""
	if n := width - xansi.StringWidth(raw); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	arrow := m.theme.Muted.Faint(true).Render(glyphArrow + " ")
	name := m.theme.Accent.Bold(true).Render(agent)
	return arrow + name + pad
}

// peerSepRule is a faint horizontal rule drawn between peer blocks so the
// boundary between two agents' text is visible without a heavy blank gap.
func (m *model) peerSepRule(inner int) string {
	// Span the peer body width so the rule actually bounds the block it
	// separates — a fixed short cap left a stubby dash floating under a
	// full-width body on normal (80-col) and wide terminals.
	n := inner
	if n < 4 {
		n = 4
	}
	return m.rolePrefix(false) + m.theme.Muted.Render(strings.Repeat("\u2500", n))
}

func peerLiveBody(b *peerLiveBuf, inner int) string {
	if b == nil || b.buf == "" {
		return ""
	}
	if b.body != "" && b.bodySrc == b.buf {
		return b.body
	}
	if b.body != "" && b.bodySrc != "" && strings.HasPrefix(b.buf, b.bodySrc) {
		tail := b.buf[len(b.bodySrc):]
		var out strings.Builder
		out.WriteString(b.body)
		if tail != "" {
			switch {
			case strings.HasSuffix(b.bodySrc, "\n") && !strings.HasSuffix(b.body, "\n"):
				out.WriteByte('\n')
			case strings.HasSuffix(b.bodySrc, " ") && !strings.HasSuffix(b.body, " ") && !strings.HasSuffix(b.body, "\n"):
				out.WriteByte(' ')
			case !strings.HasSuffix(b.body, "\n") && strings.Contains(tail, "\n"):
				out.WriteByte('\n')
			}
			out.WriteString(wordWrap(tail, inner))
		}
		return out.String()
	}
	return wordWrap(b.buf, inner)
}

func (m *model) drainPeerIngest() {
	for _, d := range m.peerIngest.take() {
		m.appendPeerDelta(d.agent, d.text)
	}
}

// finishPeerStream commits one peer's live answer (agent empty = all peers).
func (m *model) finishPeerStream(agent string) tea.Cmd {
	var cmds []tea.Cmd
	commitOne := func(b *peerLiveBuf, key string) {
		if b == nil {
			return
		}
		peer := strings.TrimRight(b.full, " \t\r\n")
		if peer != "" {
			name := b.agent
			if name == "" {
				name = "peer"
			}
			m.add(kindStatus, name+" · reply")
			idx, needsPretty := m.commitAssistant(peer)
			if needsPretty {
				cmds = append(cmds, m.kickEntryPretty(idx, m.entries[idx].text, max(24, m.vp.Width()-2)))
			}
		}
		delete(m.peerBufs, key)
		out := m.peerOrder[:0]
		for _, k := range m.peerOrder {
			if k != key {
				out = append(out, k)
			}
		}
		m.peerOrder = out
	}

	if agent != "" {
		key := peerKey(agent)
		// Events can arrive faster than Bubble Tea updates. Drain the external
		// unbounded ingest before committing so the final reply is complete.
		m.drainPeerIngest()
		if b := m.peerBufs[key]; b != nil {
			commitOne(b, key)
		} else if len(m.peerBufs) == 1 {
			for k, b := range m.peerBufs {
				commitOne(b, k)
			}
		}
	} else {
		m.drainPeerIngest()
		for _, key := range append([]string(nil), m.peerOrder...) {
			commitOne(m.peerBufs[key], key)
		}
		m.clearPeerBufs()
	}
	if len(m.peerBufs) == 0 {
		m.clearPeerBufs()
	}
	return tea.Batch(cmds...)
}
