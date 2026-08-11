package mowi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/subosito/mow"
)

// scrollViewport handles configured scroll keys and stops stream follow-scroll.
// Defaults are half-page ctrl+u / ctrl+d (laptops often lack PgUp/PgDn).
func (m *model) scrollViewport(msg tea.KeyPressMsg) tea.Cmd {
	before := m.vp.YOffset()
	keyStr := msg.String()
	ks := m.cfg.Keys
	var cmd tea.Cmd
	switch {
	case ks.Matches(ks.ScrollUp, keyStr):
		m.vp.HalfPageUp()
	case ks.Matches(ks.ScrollDown, keyStr):
		m.vp.HalfPageDown()
	default:
		m.vp, cmd = m.vp.Update(msg)
		m.followBottom = m.vp.AtBottom()
		return tea.Batch(cmd, m.afterScrollPretty())
	}
	if m.vp.YOffset() < before {
		// User moved up — pin view; stream growth must not yank to bottom.
		m.followBottom = false
	} else {
		m.followBottom = m.vp.AtBottom()
	}
	return m.afterScrollPretty()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.diffView != nil {
			// Rebuild split/unified geometry for the new width; fall back to
			// unified automatically when the terminal is too narrow.
			m.layoutDiffOverlay()
			return m, nil
		}
		// Plain re-wrap immediately; async glamour after resize settle.
		w := max(24, m.vp.Width()-2)
		inner := max(16, w-roleGutterW)
		m.streamBody = ""
		m.streamBodySrc = ""
		m.streamFrame = ""
		m.streamFrameW = 0
		// Invalidate in-flight live glamour built for the old width.
		m.streamGen++
		m.invalidateHistoryCache()
		for i := range m.entries {
			e := &m.entries[i]
			if e.kind == kindAssistant && strings.TrimSpace(e.text) != "" {
				e.view = m.renderTurn(false, wordWrap(e.text, inner), e.at, w)
				e.viewW = w
			} else {
				// Force re-render (user stamps etc.) after width change.
				e.view = ""
				e.viewW = 0
			}
		}
		var cmds []tea.Cmd
		if m.busy && (m.streamBuf != "" || m.reasonBuf != "") {
			m.paintLiveStream()
			if m.streamBuf != "" {
				cmds = append(cmds, m.kickStreamRender())
			}
		} else {
			m.refreshVP()
		}
		cmds = append(cmds, m.scheduleResizeSettle())
		return m, tea.Batch(cmds...)

	case resizeSettleMsg:
		return m, m.handleResizeSettle(msg)

	case tea.KeyPressMsg:
		ks := m.cfg.Keys
		keyStr := msg.String()
		if m.diffView != nil {
			if handled, cmd := m.handleDiffOverlayKey(keyStr, msg); handled {
				return m, cmd
			}
		}
		if m.effortPick != nil {
			return m, m.handleEffortPickKey(keyStr, msg)
		}
		if m.modelPick != nil {
			return m, m.handleModelPickKey(keyStr, msg)
		}
		if m.showHelp {
			// Any of cancel/quit/send/? closes help (keep dismiss cheap).
			if ks.Matches(ks.Cancel, keyStr) || ks.Matches(ks.Quit, keyStr) ||
				ks.Matches(ks.Send, keyStr) || keyStr == "q" || keyStr == "?" || keyStr == "/" {
				m.showHelp = false
			}
			// While busy, Esc (cancel) and the configured Quit key must stop
			// the running turn, not just dismiss the overlay — otherwise
			// opening help traps the user: the first Esc closes help and the
			// turn looks unstoppable. Mirror the normal busy cancel/quit path
			// (drop queued messages, surface peer teardown, cancel the run
			// ctx). Never quit the app here; the idle Quit path owns that.
			if m.busy && (ks.Matches(ks.Cancel, keyStr) || ks.Matches(ks.Quit, keyStr)) {
				m.dropQueue()
				m.noteCancelPeers()
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			return m, nil
		}
		if m.permWait != nil {
			return m, m.handlePermKey(keyStr)
		}
		// Configurable global bindings (idle + busy unless noted).
		switch {
		case ks.Matches(ks.SelectMode, keyStr):
			// Mouse tracking steals drag-select from the terminal. Releasing
			// it at runtime lets the user copy text without restarting; the
			// wheel falls back to the arrow-burst guard while off. The header
			// chip is the persistent state signal; the transcript only teaches
			// the mechanics once per session (queueTeachShown pattern), so
			// repeated toggles do not spam scrollback.
			m.mouseOff = !m.mouseOff
			if m.mouseOff && !m.selectTeachShown {
				m.selectTeachShown = true
				m.add(kindStatus, "select mode — mouse released, drag to select ("+
					m.cfg.Keys.Primary(m.cfg.Keys.SelectMode)+" to resume scroll)")
			}
			m.layout()
			m.refreshVP()
			return m, nil
		case ks.Matches(ks.PeerExpand, keyStr):
			// Collapsed (default) keeps a delegate's stream to one line: the
			// transcript stops repainting on every chunk, so it neither
			// flickers nor tears a text selection out from under the mouse.
			m.peerExpanded = !m.peerExpanded
			if len(m.peerBufs) == 0 {
				// Nothing streaming — say what the toggle did, or it looks dead.
				m.add(kindStatus, "peer output: "+peerModeLabel(m.peerExpanded))
			}
			for _, b := range m.peerBufs {
				// Force a rebuild: summary and body are different shapes.
				b.dirty, b.body, b.bodySrc = true, "", ""
			}
			m.invalidateStreamFrame()
			m.layout()
			m.refreshVP()
			return m, nil
		case ks.Matches(ks.Quit, keyStr):
			if m.busy {
				m.dropQueue()
				m.noteCancelPeers()
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case ks.Matches(ks.Cancel, keyStr):
			if m.busy {
				m.dropQueue()
				m.noteCancelPeers()
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			if m.editingPrompt {
				// Cancel arrow-up recall / /edit: drop the draft and
				// restore the blank prompt.
				m.editingPrompt = false
				m.ta.Reset()
				m.syncInputHeight()
				m.syncInputChrome()
				m.add(kindStatus, "edit canceled")
				m.refreshVP()
				return m, nil
			}
			if m.showWelcome {
				m.showWelcome = false
				return m, nil
			}
		case ks.Matches(ks.PermCycle, keyStr):
			m.togglePerm()
			return m, nil
		case ks.Matches(ks.Thinking, keyStr):
			// Reserved: thinking is indicator-only (no body to expand).
			return m, nil
		case ks.Matches(ks.EffortCycle, keyStr):
			return m, m.cmdEffort("")
		case ks.Matches(ks.Focus, keyStr):
			m.toggleFocus()
			return m, nil
		case ks.Matches(ks.Clear, keyStr):
			if m.busy {
				return m, nil
			}
			m.clearTranscript()
			return m, nil
		case ks.Matches(ks.Help, keyStr):
			if m.modelPick == nil && m.diffView == nil {
				// Help is allowed while busy: a user is most likely to need it
				// when something unfamiliar is happening during a turn. The
				// overlay is read-only and dismissible (see the showHelp block
				// above), so it never interferes with the running turn.
				m.showHelp = true
			}
			return m, nil
		case keyStr == "?" && m.modelPick == nil && m.diffView == nil && strings.TrimSpace(m.ta.Value()) == "":
			// Soft help when input empty (not configurable — avoids stealing typing).
			// Allowed while busy for the same reason as the Help key above.
			m.showHelp = true
			return m, nil
		case ks.Matches(ks.ViewDiff, keyStr):
			// Expand the latest write/edit card. Allowed while busy so a
			// mid-turn edit can be reviewed without waiting for the turn to end.
			if m.modelPick != nil || m.effortPick != nil || m.showHelp {
				return m, nil
			}
			if !m.toggleDiffOverlay() {
				m.add(kindStatus, "no diff to expand")
				m.refreshVP()
			}
			return m, nil
		case ks.Matches(ks.Send, keyStr):
			m.editingPrompt = false
			if m.busy {
				// A /steer draft is injected into the running turn; anything
				// else queues to send when the turn ends.
				if arg, ok := parseSteer(strings.TrimSpace(m.ta.Value())); ok {
					m.ta.Reset()
					m.syncInputHeight()
					return m, m.doSteer(arg)
				}
				return m, m.queueDraft()
			}
			return m.submit()
		case ks.Matches(ks.ScrollUp, keyStr) || ks.Matches(ks.ScrollDown, keyStr):
			return m, m.scrollViewport(msg)
		}
		// Transcript focus: printable keys do not enter the editor.
		if m.focus == focusTranscript && m.permWait == nil && !m.showHelp {
			// Allow scroll keys already handled; drop typing.
			if len(msg.Text) > 0 || keyStr == "space" || keyStr == "enter" || keyStr == "backspace" {
				return m, nil
			}
		}

	case busyHeartbeatMsg:
		// Sole driver of spinner + elapsed while busy (see scheduleBusyHeartbeat).
		if !m.busy {
			return m, nil
		}
		m.advanceSpinnerFrame()
		m.drainPeerIngest()
		m.syncInputChrome()
		// Refresh thinking indicator elapsed (one line — cheap).
		if m.reasonBuf != "" && m.streamBuf == "" {
			m.paintLiveStream()
		}
		// Activity band owns spinner/elapsed; View re-reads state each frame.
		return m, m.scheduleBusyHeartbeat()

	case spinner.TickMsg:
		// Ignore bubbles' own tick chain while busy — heartbeat owns animation.
		// Consume the message so it does not fall through to the textarea.
		if m.busy {
			return m, nil
		}
		return m, nil

	case modelListMsg:
		m.applyModelList(msg)
		m.refreshVP()
		return m, nil

	case effortMsg:
		// Async result of /effort <level> or the ctrl+e cycle: report
		// errors and confirm the applied level (picker path sets directly).
		switch {
		case msg.err != nil:
			m.add(kindError, "effort: "+msg.err.Error())
		case msg.setTo != "":
			m.add(kindStatus, "effort → "+msg.setTo)
		}
		m.layout()
		m.refreshVP()
		return m, nil

	case streamPaintMsg:
		// Throttled: rebuild frame if thinking changed; kick glamour if answer grew.
		if !m.busy {
			m.streamPaint = false
			return m, nil
		}
		var cmds []tea.Cmd
		cmds = append(cmds, m.scheduleStreamPaint())
		// Cheap frame paint when thinking changes or answer has no glamour yet.
		if m.reasonDirty || (m.streamBuf != "" && m.streamBody == "") {
			m.paintLiveStream()
		}
		if m.liveRenderPending() && !m.streamRenderBusy {
			m.streamDirty = false
			if cmd := m.kickStreamRender(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case streamRenderedMsg:
		m.streamRenderBusy = false
		if !m.busy || msg.gen != m.streamGen {
			if m.busy && m.liveRenderPending() {
				m.streamDirty = false
				return m, m.kickStreamRender()
			}
			return m, nil
		}
		if msg.peerKey != "" {
			if b := m.peerBufs[msg.peerKey]; b != nil && b.buf == msg.src {
				b.body = msg.body
				b.bodySrc = msg.src
				b.dirty = false
			}
		} else {
			m.streamBody = msg.body
			m.streamBodySrc = msg.src
			m.streamDirty = false
		}
		m.paintLiveStream()
		var cmds []tea.Cmd
		if cmd := m.kickStreamRender(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

	case entryPrettyMsg:
		// Async pretty for a finished assistant bubble (after turn, not during stream).
		if msg.idx < 0 || msg.idx >= len(m.entries) {
			return m, nil
		}
		e := &m.entries[msg.idx]
		if e.kind != kindAssistant || e.text != msg.src {
			return m, nil
		}
		if msg.body != "" {
			e.view = m.renderTurn(false, msg.body, e.at, msg.width)
			e.viewW = msg.width
			e.plain = false
			m.invalidateHistoryCache()
			m.refreshVP()
		}
		return m, nil

	case streamSnapMsg:
		// Progressive stream: always cheap-paint (stable-prefix + plain tail);
		// full glamour advances the prefix on a throttle (single-flight).
		// gen guard: a snap taken before the previous turn's doneMsg must not
		// bleed its tokens into this turn.
		if !m.busy {
			return m, nil
		}
		if msg.gen != m.turnGen {
			// Stale snap (turn boundary) — keep polling with the live gen or
			// the stream freezes mid-turn with no further tokens.
			return m, m.pollStream()
		}
		m.applyStreamSnap(msg.content, msg.reasoning)
		// History cache stays put; only streamFrame rebuilds.
		m.paintLiveStream()
		var cmds []tea.Cmd
		if !msg.finished {
			cmds = append(cmds, m.pollStream())
		}
		if cmd := m.ensureStreamPaint(); cmd != nil {
			cmds = append(cmds, cmd)
		} else if m.streamDirty && !m.streamRenderBusy {
			m.streamDirty = false
			if cmd := m.kickStreamRender(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case reasoningMsg:
		// Test / single-piece path.
		if !m.busy {
			return m, nil
		}
		m.applyStreamSnap("", string(msg))
		m.paintLiveStream()
		return m, m.ensureStreamPaint()

	case deltaMsg:
		if !m.busy {
			return m, nil
		}
		m.applyStreamSnap(string(msg), "")
		m.paintLiveStream()
		return m, m.ensureStreamPaint()

	case permAskMsg:
		m.armPermWait(&msg)
		// Cap preview size so a huge write/edit does not freeze View() every frame.
		args := msg.args
		if xansi.StringWidth(args) > 4000 {
			args = xansi.Truncate(args, 4000, "\n…(preview truncated)")
		}
		m.add(kindPerm, fmt.Sprintf("%s\n%s\n\ny allow · n deny · a always",
			msg.name, args))
		m.layout()
		m.refreshVP()
		// Keep tool/perm/polls alive — returning bare nil drops Blink and can
		// make the UI feel frozen while waiting for y/n.
		return m, tea.Batch(m.pollToolUI(), textarea.Blink)

	case toolUIMsg:
		if msg.turnText != "" {
			// Turn boundary: commit the narration as a real entry and reset
			// the live stream so the next turn starts fresh (no cross-turn
			// weld, no vanishing text at run end).
			if m.busy {
				idx, needsPretty := m.commitAssistant(strings.TrimRight(msg.turnText, " \t\r\n"))
				m.clearLiveStream()
				m.refreshVP()
				if needsPretty {
					return m, tea.Batch(m.pollToolUI(), m.kickEntryPretty(idx, m.entries[idx].text, max(24, m.vp.Width()-2)))
				}
			}
			return m, m.pollToolUI()
		}
		if msg.compactDone {
			m.ctxPressureBand = 0
			m.layout()
			return m, m.pollToolUI()
		}
		if msg.peerUsage.in > 0 || msg.peerUsage.out > 0 {
			m.peerTokIn += msg.peerUsage.in
			m.peerTokOut += msg.peerUsage.out
			m.refreshVP()
			return m, m.pollToolUI()
		}
		if msg.streamDelta != "" {
			// Peer acp_delegate answer chunks — per-agent buffer.
			if m.busy && m.peerLive.Load() {
				agent := msg.peerAgent
				if agent == "" {
					if v, ok := m.peerAgent.Load().(string); ok {
						agent = v
					}
				}
				m.drainPeerIngest()
				m.appendPeerDelta(agent, msg.streamDelta)
				m.paintLiveStream()
				return m, tea.Batch(m.pollToolUI(), m.ensureStreamPaint())
			}
			return m, m.pollToolUI()
		}
		if msg.start {
			// Live indicator only — painted by the busy heartbeat.
			// Also used for peer progress labels ("claude: read …").
			if msg.clearStream {
				// acp_delegate about to run: host narration is committed;
				// wipe host live buffers so peer text starts clean.
				if m.ingest != nil {
					_, _, _ = m.ingest.take() // discard stray host tokens
				}
				m.clearLiveStream()
				if m.peerActive.Load() <= 0 || !msg.peerArmed {
					m.peerActive.Add(1)
				}
				m.peerLive.Store(true)
				agent := msg.peerAgent
				if agent == "" {
					if v, ok := m.peerAgent.Load().(string); ok {
						agent = v
					}
				}
				if agent == "" && strings.Contains(msg.name, ":") {
					agent = strings.TrimSpace(strings.Split(msg.name, ":")[0])
					m.peerAgent.Store(agent)
				}
				m.ensurePeerBuf(agent)
				m.paintLiveStream()
			} else if m.peerLive.Load() {
				// Progress label while peer window is open only.
			} else if strings.Contains(msg.name, ":") {
				// Straggler peer progress after endPeer — ignore spinner update.
				return m, m.pollToolUI()
			}
			m.toolCurrent = msg.name
			m.toolCurrentArgs = msg.args
			m.lastActivityAt = time.Now()
			m.syncInputChrome()
			m.layout()
			return m, m.pollToolUI()
		}
		// Tool finished: update the per-turn tally line in place (errors get
		// their own entry — they must stay visible), plus diffs for write/edit.
		if msg.lsp != nil {
			m.addLSPProblems(*msg.lsp)
			m.refreshVP()
			return m, m.pollToolUI()
		}
		var prettyCmd tea.Cmd
		if msg.endPeer {
			m.drainPeerIngest()
			prettyCmd = m.finishPeerStream(msg.peerAgent)
			n := m.peerActive.Add(-1)
			if n < 0 {
				m.peerActive.Store(0)
				n = 0
			}
			if n > 0 {
				// Parallel peer still running — keep accepting chunks.
				m.peerLive.Store(true)
				m.toolCurrent = ""
				m.toolCurrentArgs = ""
			} else {
				// All peers done; host model may still be synthesizing.
				m.peerLive.Store(false)
				m.clearPeerBufs()
				m.toolCurrent = "writing"
				m.toolCurrentArgs = ""
			}
			m.lastActivityAt = time.Now()
			m.syncInputChrome()
		} else {
			m.toolCurrent = ""
			m.toolCurrentArgs = ""
			m.lastActivityAt = time.Now()
			m.syncInputChrome()
		}
		changed := false
		if strings.TrimSpace(msg.line) != "" {
			if msg.isErr {
				m.bumpToolError(msg.line)
			} else {
				m.bumpToolTally(msg.name, msg.line)
			}
			changed = true
		}
		if strings.TrimSpace(msg.text) != "" {
			m.add(kindDiff, msg.text)
			changed = true
		}
		if changed || prettyCmd != nil {
			// Keep following live stream if we were already at the bottom.
			m.refreshVP()
		}
		return m, tea.Batch(m.pollToolUI(), prettyCmd)

	case goalEventMsg:
		return m, m.handleGoalEvent(msg.ev)

	case goalDoneMsg:
		return m, m.handleGoalDone(msg)

	case packSlashDoneMsg:
		return m, m.applyPackSlashDone(msg)

	case recallConfirmMsg:
		// Mouse-off wheel guard: the up-arrow recall was held for the confirm
		// window. It fires only if it is STILL pending (a wheel burst's second
		// arrow clears it) and the prompt is still empty and idle.
		if !m.pendingRecall {
			return m, nil
		}
		m.pendingRecall = false
		if !m.busy && strings.TrimSpace(m.ta.Value()) == "" &&
			time.Since(m.pendingRecallAt) >= recallConfirmWindow {
			return m, m.editLast()
		}
		return m, nil

	case doneMsg:
		m.busy = false
		m.editingPrompt = false
		m.cancel = nil
		m.toolCurrent = ""
		m.toolCurrentArgs = ""
		m.tokIn += msg.usage.InputTokens
		m.tokOut += msg.usage.OutputTokens
		m.eng.SetOnToken(nil)
		m.eng.SetOnReasoning(nil)
		m.liveIngest.Store(nil)
		// Drain leftover tokens first, then close any open peer stream so host
		// final answer does not weld onto peer markdown.
		if m.ingest != nil {
			c, r, _ := m.ingest.take()
			m.applyStreamSnap(c, r)
			m.ingest = nil
		}
		var prettyCmd tea.Cmd
		var peerPrettyCmd tea.Cmd
		if m.peerLive.Load() || m.peerActive.Load() > 0 || len(m.peerBufs) > 0 {
			m.drainPeerIngest()
			peerPrettyCmd = m.finishPeerStream("")
			m.peerLive.Store(false)
			m.peerActive.Store(0)
			m.clearPeerBufs()
		}
		final := strings.TrimRight(msg.text, " \t\r\n")
		// On error/cancel the engine does NOT record the partial reply in its
		// history — committing streamBuf here would leave the transcript with
		// an assistant turn the engine never saw, so the UI would lie about
		// the conversation (and diverge from the engine on the next prompt).
		// Live tokens already streamed to the screen; drop them on failure.
		if final == "" && msg.err == nil {
			final = strings.TrimRight(m.streamBuf, " \t\r\n")
		}
		// Never commit thinking tags / reasoning into the transcript entry.
		if vis, th := mow.StripThinking(final); th != "" || vis != final {
			final = vis
		}
		// Reasoning is live-only. Prefer live glamour; else plain + async pretty.
		if final != "" {
			idx, needsPretty := m.commitAssistant(final)
			if needsPretty {
				prettyCmd = m.kickEntryPretty(idx, final, max(24, m.vp.Width()-2))
			}
		}
		m.resetStreamState()
		if msg.err != nil {
			errStr := strings.ToLower(msg.err.Error())
			switch {
			case errors.Is(msg.err, context.Canceled) || strings.Contains(errStr, "context canceled"):
				m.add(kindStatus, "cancelled")
			case errors.Is(msg.err, context.DeadlineExceeded) || strings.Contains(errStr, "context deadline exceeded"):
				m.add(kindStatus, "timed out")
			default:
				m.add(kindError, msg.err.Error())
			}
		}
		m.maybeCtxPressureStatus()
		m.syncInputChrome() // restore ❯
		// Strip any mouse/CSI garbage that leaked into the draft while frozen.
		m.sanitizeInput()
		m.layout()
		m.refreshVP()
		// Auto-send the next queued message, if any.
		if len(m.queued) > 0 {
			if _, cmd := m.dequeue(); cmd != nil {
				return m, tea.Batch(m.pollPerm(), m.pollToolUI(), cmd, prettyCmd, peerPrettyCmd)
			}
		}
		// Re-arm cursor blink — we drop BlinkMsg while busy, so without this
		// the input looks dead after the first reply.
		return m, tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink, prettyCmd, peerPrettyCmd)
	}

	// Typing: idle and busy (draft next message while the turn runs).
	// Letter keys stay with the textarea; scroll keys handled above.
	// Diff overlay is exclusive: keys are handled above, but keep typing
	// gated here so a fall-through never edits the draft under the frame.
	canType := m.permWait == nil && !m.showHelp && m.modelPick == nil && m.effortPick == nil && m.diffView == nil && m.focus == focusEditor
	// Mouse belongs exclusively to the transcript viewport (or the diff
	// overlay viewport when open). Passing wheel or click messages through
	// textarea.Update first can move its internal view/cursor even though
	// the draft text is unchanged.
	switch msg.(type) {
	case tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseClickMsg, tea.MouseReleaseMsg:
		canType = false
	}
	if canType {
		if km, ok := msg.(tea.KeyPressMsg); ok {
			if keyLooksLikeMouseLeak(km) {
				// Drop SGR mouse / CSI fragments so they never enter the prompt.
				return m, nil
			}
			// Any non-arrow key cancels a held recall (mouse-off confirm window).
			if m.pendingRecall && km.Code != tea.KeyUp && km.Code != tea.KeyDown {
				m.pendingRecall = false
			}
			// Arrow bursts are wheel noise when mouse tracking is off
			// (MOW_MOUSE=0): terminals translate scroll into rapid
			// KeyUp/KeyDown sequences. Drop the burst before it can recall
			// a prompt or walk the draft cursor.
			if !m.mouseOn() && (km.Code == tea.KeyUp || km.Code == tea.KeyDown) && m.arrowBurst() {
				m.pendingRecall = false
				return m, nil
			}
			// ↑ on an empty prompt recalls the last message for editing (shell-style).
			// (Model picker owns ↑ while open — see handleModelPickKey.)
			// Wheel events are consumed before this path, but some terminals
			// leak wheel-up as an up-arrow escape right after a mouse event —
			// the grace window keeps recall arrow-key-only.
			if km.Code == tea.KeyUp && !m.busy && strings.TrimSpace(m.ta.Value()) == "" &&
				time.Since(m.lastMouseAt) > 150*time.Millisecond {
				if m.mouseOn() {
					return m, m.editLast()
				}
				// Mouse off: a single up-arrow may be the FIRST event of a
				// wheel burst (no MouseWheelMsg ever arrives, so the grace
				// window above can't arm). Hold the recall for a short confirm
				// window; the next arrow of the spin cancels it, a lone press
				// fires it (recallConfirmMsg / arrowBurst).
				m.lastArrowAt = time.Now()
				m.pendingRecall = true
				m.pendingRecallAt = time.Now()
				return m, tea.Tick(recallConfirmWindow, func(time.Time) tea.Msg { return recallConfirmMsg{} })
			}
		}
		// Cap before Update so DynamicHeight recalculates with the right MaxHeight.
		m.applyInputHeightCap()
		beforeH := m.ta.Height()
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		cmds = append(cmds, cmd)
		m.syncInputChrome()
		// DynamicHeight may have grown/shrunk for newlines or soft-wrap.
		if m.ta.Height() != beforeH || m.clampInputHeight() {
			m.layout()
			if m.followBottom {
				m.vp.GotoBottom()
			}
		}
	}
	// Mouse: wheel scrolls the active viewport only — never the draft, and
	// never the transcript under a full-screen diff overlay. Motion/click
	// are dropped so they never flood Update.
	if m.mouseOn() {
		switch msg.(type) {
		case tea.MouseWheelMsg:
			m.lastMouseAt = time.Now() // wheel activity: arm the KeyUp grace window
			if m.diffView != nil {
				var cmd tea.Cmd
				m.diffView.vp, cmd = m.diffView.vp.Update(msg)
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}
			before := m.vp.YOffset()
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			cmds = append(cmds, cmd)
			if m.vp.YOffset() < before {
				m.followBottom = false
			} else {
				m.followBottom = m.vp.AtBottom()
			}
			if m.vp.YOffset() != before {
				cmds = append(cmds, m.afterScrollPretty())
			}
		case tea.MouseMotionMsg, tea.MouseClickMsg, tea.MouseReleaseMsg:
			m.lastMouseAt = time.Now()
			return m, tea.Batch(cmds...)
		}
	}
	return m, tea.Batch(cmds...)
}

// keyLooksLikeMouseLeak reports SGR mouse / broken CSI fragments misread as keys.
// Example leak: "[<64;24;27M" (wheel) when the event loop was stalled.
func keyLooksLikeMouseLeak(km tea.KeyPressMsg) bool {
	s := km.String()
	if s == "" {
		return false
	}
	if strings.Contains(s, "[<") || strings.Contains(s, "\x1b[<") {
		return true
	}
	// Printable text that is only mouse-report junk.
	if r := km.Text; r != "" {
		if strings.Contains(r, "[<") {
			return true
		}
		// Sequences often arrive split: "<64;24;27M"
		if strings.ContainsAny(r, "<>") && strings.Contains(r, ";") && strings.ContainsAny(r, "Mm") {
			return true
		}
	}
	return false
}

// sanitizeInput strips leaked mouse CSI fragments from the draft textarea.
func (m *model) sanitizeInput() {
	v := m.ta.Value()
	if v == "" || (!strings.Contains(v, "[<") && !strings.Contains(v, "<")) {
		return
	}
	// Drop SGR mouse report patterns: CSI < btn ; x ; y M/m (and bare [<…M).
	cleaned := mouseLeakRe.ReplaceAllString(v, "")
	if cleaned != v {
		m.ta.SetValue(cleaned)
	}
}

// recallConfirmWindow is how long an up-arrow prompt recall is held when mouse
// tracking is off (MOW_MOUSE=0), waiting to see whether a second arrow follows
// (a wheel spin) and cancels it. 90ms is imperceptible for a deliberate press
// but catches every real wheel notch.
const recallConfirmWindow = 90 * time.Millisecond

// arrowBurst reports whether this arrow key is part of a rapid burst — the
// shape a terminal emits when it translates the wheel into keys because mouse
// tracking is off (MOW_MOUSE=0). A deliberate press is a single event; a wheel
// notch emits several arrows within a few ms. Only arrows are tracked, so
// typing cadence never trips it.
func (m *model) arrowBurst() bool {
	now := time.Now()
	burst := now.Sub(m.lastArrowAt) < 80*time.Millisecond
	m.lastArrowAt = now
	return burst
}
