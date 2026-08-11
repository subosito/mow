package mowi

import (
	"context"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/subosito/mow"
)

// peerDeltaIngest batches peer answer chunks outside Bubble Tea's bounded
// message channel. The ACP event callback must never drop model output or
// block the engine goroutine; Update drains this buffer on its regular paint
// heartbeat and before committing an endPeer event.
type peerDeltaIngest struct {
	mu     sync.Mutex
	parts  map[string]string
	agents map[string]string
	order  []string
}

func newPeerDeltaIngest() *peerDeltaIngest {
	return &peerDeltaIngest{
		parts:  make(map[string]string),
		agents: make(map[string]string),
	}
}

func (p *peerDeltaIngest) push(agent, delta string) {
	if p == nil || delta == "" {
		return
	}
	key := peerKey(agent)
	p.mu.Lock()
	if _, ok := p.parts[key]; !ok {
		p.order = append(p.order, key)
		p.agents[key] = strings.TrimSpace(agent)
	}
	p.parts[key] += delta
	p.mu.Unlock()
}

func (p *peerDeltaIngest) take() []peerDelta {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]peerDelta, 0, len(p.order))
	for _, key := range p.order {
		if text := p.parts[key]; text != "" {
			out = append(out, peerDelta{agent: p.agents[key], text: text})
		}
	}
	p.parts = make(map[string]string)
	p.agents = make(map[string]string)
	p.order = nil
	return out
}

func (p *peerDeltaIngest) clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.parts = make(map[string]string)
	p.agents = make(map[string]string)
	p.order = nil
	p.mu.Unlock()
}

type peerDelta struct {
	agent string
	text  string
}

// peerPhase is operational state for a collapsed peer summary line.
// Never holds chain-of-thought text — only what the peer is doing.
type peerPhase int

const (
	peerPhaseWaiting  peerPhase = iota // armed, no thought/tool signal yet
	peerPhaseThinking                  // agent_thought_chunk / thought progress
	peerPhaseTool                      // tool_call / tool progress
)

// peerLiveBuf is one in-flight acp_delegate answer, keyed by agent name.
type peerLiveBuf struct {
	agent     string
	buf       string    // bounded display buffer; full is committed
	full      string    // complete sanitized answer, never trimmed for live paint
	body      string    // last markdown-rendered answer body
	bodySrc   string    // source snapshot for body
	dirty     bool      // answer needs a markdown render
	startedAt time.Time // when the peer slot opened (elapsed in pre-answer notes)
	phase     peerPhase // operational state before answer text arrives
}

// streamIngest collects SSE tokens from the LLM goroutine without blocking it.
// The UI drains snapshots via pollStream — never one Bubble Tea message per token.
type streamIngest struct {
	mu        sync.Mutex
	content   string
	reasoning string
	done      bool
	sig       chan struct{} // capacity 1
}

func newStreamIngest() *streamIngest {
	return &streamIngest{sig: make(chan struct{}, 1)}
}

func (s *streamIngest) ping() {
	select {
	case s.sig <- struct{}{}:
	default:
	}
}

func (s *streamIngest) pushContent(d string) {
	if s == nil || d == "" {
		return
	}
	s.mu.Lock()
	s.content += d
	s.mu.Unlock()
	s.ping()
}

func (s *streamIngest) pushReasoning(d string) {
	if s == nil || d == "" {
		return
	}
	s.mu.Lock()
	s.reasoning += d
	s.mu.Unlock()
	s.ping()
}

func (s *streamIngest) finish() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
	s.ping()
}

// take drains buffered text. finished is true when the LLM side called finish()
// (caller may still need to apply the returned content/reasoning first).
func (s *streamIngest) take() (content, reasoning string, finished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content = s.content
	reasoning = s.reasoning
	s.content = ""
	s.reasoning = ""
	finished = s.done
	return content, reasoning, finished
}

// streamPaintInterval throttles live glamour kicks so spinner stays free.
const streamPaintInterval = 80 * time.Millisecond

// scheduleStreamPaint arms the next paint-scheduler tick.
func (m *model) scheduleStreamPaint() tea.Cmd {
	return tea.Tick(streamPaintInterval, func(time.Time) tea.Msg {
		return streamPaintMsg{}
	})
}

func (m *model) liveRenderPending() bool {
	if m.streamDirty {
		return true
	}
	for _, key := range m.peerOrder {
		if b := m.peerBufs[key]; b != nil && b.dirty && b.buf != "" {
			return true
		}
	}
	return false
}

// kickStreamRender glamours live answer content off Update (single-flight).
// Host streamBuf and dirty peer buffers share the same live=true path
// (live=true stabilizeMarkdown closes open fences so partial MD stays
// readable). Peer buffers are skipped while collapsed: their text is not on
// screen, so rendering it would burn a glamour pass per chunk and race the
// summary line for the same cached body.
func (m *model) kickStreamRender() tea.Cmd {
	hostNeed := m.streamBuf != "" && (m.streamDirty || m.streamBody == "" || m.streamBodySrc != m.streamBuf)
	var pkey, pbuf string
	if !m.peerLiveCollapsed() {
		for _, key := range m.peerOrder {
			if b := m.peerBufs[key]; b != nil && b.dirty && b.buf != "" {
				pkey, pbuf = key, b.buf
				break
			}
		}
	}
	if !hostNeed && pkey == "" {
		return nil
	}
	if m.streamRenderBusy {
		m.streamDirty = true
		return nil
	}
	m.streamRenderBusy = true
	m.streamGen++
	gen := m.streamGen
	w := max(24, m.vp.Width()-2)
	inner := max(16, w-roleGutterW)
	if hostNeed {
		md := &m.md
		buf := m.streamBuf
		return func() tea.Msg {
			body := renderMarkdownCached(md, buf, inner, true /* live fences */)
			return streamRenderedMsg{gen: gen, width: w, src: buf, body: body}
		}
	}
	// Peer progress renders through the FAINT cache: dimmed palette tokens so
	// an acp_delegate's streaming answer reads as low-priority progress, not
	// main transcript content (the committed reply renders full-strength).
	mdFaint := &m.mdFaint
	key, buf := pkey, pbuf
	return func() tea.Msg {
		body := renderMarkdownCached(mdFaint, buf, inner, true)
		return streamRenderedMsg{gen: gen, width: w, src: buf, body: body, peerKey: key}
	}
}

// paintLiveStream assembles live turn: thinking indicator + answer (+ caret).
// Thinking is indicator-only (spinner + elapsed) — never paints reasoning text.
// Answer uses roleBlock once with its own gutter.
func (m *model) paintLiveStream() {
	w := max(24, m.vp.Width()-2)
	inner := max(16, w-roleGutterW)
	thinking := strings.TrimSpace(m.reasonBuf) != ""

	// Reasoning-only phase (no answer tokens yet): spinner + elapsed only.
	if thinking && m.streamBuf == "" && len(m.peerBufs) == 0 {
		m.streamFrame = m.renderThinkingIndicator() + "\n"
		m.streamFrameW = w
		m.reasonDirty = false
		m.applyVPContent()
		return
	}

	var frame strings.Builder
	// Peer live answers (one block per agent) sit above the host live stream.
	if peers := m.peerLiveBodies(inner); peers != "" {
		frame.WriteString(peers)
		frame.WriteString("\n")
	}
	if ans := m.liveAnswerBody(inner); ans != "" {
		frame.WriteString(m.roleBlock(false, ans))
		frame.WriteString("\n")
	} else if !m.peerLive.Load() || len(m.peerBufs) == 0 {
		// Idle caret when nothing else is live.
		frame.WriteString(m.rolePrefix(false) + m.theme.Muted.Render(glyphCaret) + "\n")
	}
	m.streamFrame = frame.String()
	m.streamFrameW = w
	m.reasonDirty = false
	m.applyVPContent()
}

// liveAnswerBody builds the streaming answer region.
//
// Stable prefix: keep last glamoured prefix (streamBody for streamBodySrc),
// append plain word-wrap of the new tail only. Full re-glamour advances the
// prefix on streamRenderedMsg — never replace the whole message with an older
// truncated frame.
//
// Peer (acp_delegate) streams share this path with the host answer so live
// markdown (headings, emphasis, fences via stabilizeMarkdown) paints while
// chunks arrive; plain tail covers tokens not yet in the glamoured prefix.
func (m *model) liveAnswerBody(inner int) string {
	if m.streamBuf == "" {
		return ""
	}
	caret := m.theme.Muted.Render(" " + glyphCaret)

	// Exact match: fully pretty.
	if m.streamBody != "" && m.streamBodySrc == m.streamBuf {
		return m.streamBody + caret
	}

	// Stable prefix: glamoured head + plain tail for new tokens.
	if m.streamBody != "" && m.streamBodySrc != "" && strings.HasPrefix(m.streamBuf, m.streamBodySrc) {
		tail := m.streamBuf[len(m.streamBodySrc):]
		var b strings.Builder
		b.WriteString(m.streamBody)
		if tail != "" {
			// Seam guard: glamour trims trailing whitespace from the rendered
			// prefix, so a tail that followed a newline/space in the source
			// would weld onto the last word ("files.Let"). Restore the
			// source's separator before appending.
			switch {
			case strings.HasSuffix(m.streamBodySrc, "\n") && !strings.HasSuffix(m.streamBody, "\n"):
				b.WriteByte('\n')
			case strings.HasSuffix(m.streamBodySrc, " ") && !strings.HasSuffix(m.streamBody, " ") && !strings.HasSuffix(m.streamBody, "\n"):
				b.WriteByte(' ')
			case !strings.HasSuffix(m.streamBody, "\n") && strings.Contains(tail, "\n"):
				// Pretty block ends mid-line and the tail is multi-line —
				// continue on a fresh visual line.
				b.WriteByte('\n')
			}
			b.WriteString(wordWrap(tail, inner))
		}
		b.WriteString(caret)
		return b.String()
	}

	// No usable prefix (first tokens or src invalidated) — full plain.
	return wordWrap(m.streamBuf, inner) + caret
}

// renderThinkingIndicator is a single cheap line: thin bar + spinner + "thinking" + elapsed.
// Reasoning tokens are never shown — only that the model is thinking.
func (m *model) renderThinkingIndicator() string {
	spin := m.spinnerView()
	el := "0.0s"
	if !m.reasonStartedAt.IsZero() {
		el = formatElapsed(time.Since(m.reasonStartedAt))
	}
	gutter := strings.Repeat(" ", roleGutterW)
	// Solid label — mowi stays quiet; the animated spinner already carries the
	// "working" peer-bion, and the elapsed timer is the heartbeat.
	// Dimmed: thinking is progress, not content — the eye should skip it
	// until the real answer lands.
	label := m.theme.Muted.Faint(true).Render("thinking " + el)
	return gutter + spin + " " + label
}

func (m *model) syncReasonBuf() {
	// Keep a short marker only (presence for the indicator). Full token streams
	// are not stored for display — avoids ram + accidental paint of glued tokens.
	if m.reasonAPI != "" || m.reasonFromTags != "" {
		m.reasonBuf = "."
	} else {
		m.reasonBuf = ""
	}
}

// applyStreamSnap merges a batched content/reasoning drain into the model.
//
// Reasoning (SSE reasoning / reasoning_content / Anthropic thinking_delta):
//
//	arms the spinner only — token text is discarded, never painted.
//
// Content channel:
//
//	<think>…</think> (and variants) are stripped. While a think block is still
//	open, the answer pane stays empty (indicator only) so partial CoT cannot
//	leak as glued tokens like "project.Let me".
func (m *model) applyStreamSnap(content, reasoning string) {
	if content != "" || reasoning != "" {
		m.lastActivityAt = time.Now()
	}
	if reasoning != "" {
		if !m.reasoningArmed() {
			m.reasonStartedAt = time.Now()
		}
		// Presence only — do not accumulate full chain-of-thought for UI.
		if m.reasonAPI == "" {
			m.reasonAPI = "."
		}
		m.syncReasonBuf()
		m.reasonDirty = true
	}
	if content != "" {
		// Sanitize at ingestion — the live frame paints this text raw.
		content = sanitizeDisplay(content)
		// Fast path: no think tag ever seen (streamBuf mirrors streamRaw) and
		// no tag-start char in the new delta or the previous tail — skip the
		// O(len) re-extract that made huge answers quadratic. The 24-byte tail
		// window exceeds the longest open tag, so a marker split across deltas
		// still forces the slow path.
		const thinkMarkerChars = "<◁`"
		fast := m.streamBuf == m.streamRaw &&
			!strings.ContainsAny(content, thinkMarkerChars) &&
			!strings.ContainsAny(tailBytes(m.streamRaw, 24), thinkMarkerChars)
		m.streamRaw += content
		if fast {
			m.streamBuf = m.streamRaw
			m.streamDirty = true
			return
		}
		vis, th, unclosed := mow.ExtractThinking(m.streamRaw)
		if th != "" || unclosed {
			if !m.reasoningArmed() {
				m.reasonStartedAt = time.Now()
			}
			if m.reasonFromTags == "" {
				m.reasonFromTags = "."
			}
			m.syncReasonBuf()
			m.reasonDirty = true
		}
		// While think tags are still open, hide all answer text (indicator only).
		// After close, show only the non-think remainder.
		if unclosed {
			if m.streamBuf != "" {
				m.streamBuf = ""
				m.streamDirty = true
				// Invalidate any glamoured prefix built before the open tag.
				m.streamBody = ""
				m.streamBodySrc = ""
			}
		} else {
			if vis != m.streamBuf {
				m.streamBuf = vis
				if vis != "" {
					m.streamDirty = true
				}
			}
		}
	}
}

// tailBytes returns the last n bytes of s (ASCII marker scan only).
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func (m *model) reasoningArmed() bool {
	return m.reasonAPI != "" || m.reasonFromTags != "" || strings.TrimSpace(m.reasonBuf) != ""
}

// ensureStreamPaint starts the paint/glamour scheduler once.
func (m *model) ensureStreamPaint() tea.Cmd {
	if m.streamPaint {
		return nil
	}
	m.streamPaint = true
	// Immediate first paint so thinking/caret show without waiting a tick.
	m.paintLiveStream()
	var cmds []tea.Cmd
	if m.streamDirty && m.streamBuf != "" {
		m.streamDirty = false
		if cmd := m.kickStreamRender(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	cmds = append(cmds, m.scheduleStreamPaint())
	return tea.Batch(cmds...)
}

// commitAssistant installs a finished assistant entry.
// Reuse live glamour only when it was rendered from the full final text;
// otherwise plain wrap now + async pretty (avoids truncated mid-stream frames).
func (m *model) commitAssistant(final string) (idx int, needsPretty bool) {
	m.add(kindAssistant, final)
	idx = len(m.entries) - 1
	w := max(24, m.vp.Width()-2)
	if w <= 0 {
		w = 80
	}
	inner := max(16, w-roleGutterW)
	at := m.entries[idx].at
	if m.streamBody != "" && strings.TrimSpace(m.streamBodySrc) == strings.TrimSpace(final) {
		fw := m.streamFrameW
		if fw <= 0 {
			fw = w
		}
		m.entries[idx].view = m.renderTurn(false, m.streamBody, at, fw)
		m.entries[idx].viewW = fw
		m.invalidateHistoryCache()
		return idx, false
	}
	// Full final text as plain immediately; glamour catches up async.
	m.entries[idx].view = m.renderTurn(false, wordWrap(final, inner), at, w)
	m.entries[idx].viewW = w
	m.entries[idx].plain = true
	m.invalidateHistoryCache()
	return idx, true
}

// clearLiveStream drops per-stream buffers and invalidates in-flight live
// glamour (streamGen). Safe mid-run (goal steps); does not touch scheduler
// flags or turnGen.
func (m *model) clearLiveStream() {
	m.streamRaw, m.streamBuf = "", ""
	m.reasonAPI, m.reasonFromTags, m.reasonBuf = "", "", ""
	m.streamDirty, m.reasonDirty = false, false
	m.streamFrame, m.streamFrameW = "", 0
	m.streamBody, m.streamBodySrc = "", ""
	m.reasonStartedAt = time.Time{}
	m.streamGen++
}

// noteCancelPeers updates the spinner so cancel of a long acp_delegate is
// visible while the engine tears down peers (session/cancel + process kill).
func (m *model) noteCancelPeers() {
	if m.peerLive.Load() || strings.Contains(strings.ToLower(m.toolCurrent), "acp") ||
		strings.Contains(m.toolCurrent, ":") {
		m.toolCurrent = "cancelling peers…"
		m.toolCurrentArgs = ""
		m.syncInputChrome()
	}
}

// finishPeerStream commits the acp_delegate live answer as a transcript entry
// and clears the stream so the host's next tokens do not weld onto it.
// Does not clear peerLive/peerActive — caller decides (parallel peers).
// Returns an optional async pretty cmd for the committed entry.
// resetStreamState is the turn-boundary reset (submit / done / goal run+done).
// Five call sites once each hand-rolled this list and drifted — keep it here.
func (m *model) resetStreamState() {
	m.clearLiveStream()
	m.clearPeerBufs()
	m.peerIngest.clear()
	m.streamPaint = false
	m.streamRenderBusy = false
	m.peerLive.Store(false)
	m.peerActive.Store(0)
	m.turnGen++
}

// startTurn runs a prompt turn. ephemeral asides (/btw) run against current
// context but mow does not persist them, so they never re-enter a later prompt;
// mowi marks the exchange with a status line but otherwise renders it normally.
func (m *model) startTurn(text string, ephemeral bool) (tea.Model, tea.Cmd) {
	m.showWelcome = false
	if ephemeral {
		m.add(kindStatus, "btw · aside — not added to context")
	}
	m.add(kindUser, text)
	// @path references: display keeps @refs; model gets contents inlined.
	// Paths go through eng.ResolvePath (workspace + ExtraRoots).
	sent, attached := expandFileRefs(m.eng, text)
	if len(attached) > 0 {
		m.add(kindStatus, "attached "+strings.Join(attached, ", "))
	}
	m.resetStreamState()
	m.resetToolTally()
	m.busy = true
	m.followBottom = true // new turn: stick to the stream until user scrolls up
	m.lastActivityAt = time.Now()
	m.startedAt = time.Now()
	// Collapse input to one line; spinner+elapsed replace ❯ (typing still allowed).
	m.ta.SetHeight(inputMinHeight)
	m.syncInputChrome()
	m.layout()
	m.refreshVP()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	// Mutex-backed ingest: LLM never blocks on a full tea channel (that froze the
	// TUI when DeepSeek flooded reasoning tokens faster than Update could paint).
	// Peer acp_delegate chunks share this buffer via liveIngest.
	ing := newStreamIngest()
	m.ingest = ing
	m.liveIngest.Store(ing)
	m.peerLive.Store(false)
	m.peerActive.Store(0)

	if m.stream {
		m.eng.SetOnToken(ing.pushContent)
		m.eng.SetOnReasoning(ing.pushReasoning)
	} else {
		m.eng.SetOnToken(nil)
		m.eng.SetOnReasoning(nil)
	}

	opt := mow.PromptOpts{Ephemeral: ephemeral}
	return m, tea.Batch(
		// One reliable 10Hz heartbeat: spinner frames + elapsed 0.0s, 0.1s, …
		// (bubbles spin.Tick tag-chain can stop for the whole TTFT wait.)
		m.scheduleBusyHeartbeat(),
		m.pollStream(),
		func() tea.Msg {
			res, err := m.eng.PromptWith(ctx, sent, opt)
			m.eng.SetOnToken(nil)
			m.eng.SetOnReasoning(nil)
			m.liveIngest.Store(nil)
			ing.finish() // wake pollStream if waiting
			return doneMsg{text: res.Text, usage: res.Usage, err: err}
		},
	)
}

// pollStream waits for the next ingest signal, drains a batch, returns streamSnapMsg.
func (m *model) pollStream() tea.Cmd {
	ing := m.ingest
	if ing == nil {
		return nil
	}
	gen := m.turnGen
	return func() tea.Msg {
		for {
			<-ing.sig
			c, r, finished := ing.take()
			if c != "" || r != "" || finished {
				return streamSnapMsg{gen: gen, content: c, reasoning: r, finished: finished}
			}
			// Spurious wake with nothing and not finished — wait again.
		}
	}
}
