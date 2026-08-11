// Package mowi is the Bubble Tea UI for the mow headless harness
// ("mow with interface"). Import path: github.com/subosito/mow/packs/mowi
//
// Config section: extensions.tui (shared MOW_HOME with mow).
// It does not implement the agent loop; all work goes through mow.Engine.
package mowi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/goal"
)

// mouseLeakRe matches SGR mouse reports (and common partial leaks) in draft text.
var mouseLeakRe = regexp.MustCompile(`\x1b?\[?<?\d+(?:;\d+)*[Mm]|\[<\d+;\d+;\d+[Mm]`)

// PermissionMode controls power-tool prompts in the TUI.
type PermissionMode int

const (
	// PermAuto runs tools without interactive prompts (still subject to Engine policy).
	PermAuto PermissionMode = iota
	// PermAsk prompts for write/edit/bash (y/n/a = always this session).
	PermAsk
)

func (p PermissionMode) String() string {
	if p == PermAsk {
		return "ask"
	}
	return "auto"
}

// Options configures the TUI.
type Options struct {
	Engine         *mow.Engine
	AskPermissions bool
	DisableStream  bool
}

type entryKind int

const (
	kindUser entryKind = iota
	kindAssistant
	kindSystem
	kindTool
	kindError
	kindWarn
	kindStatus
	kindPerm
	// kindDiff is a write/edit file change (path + unified diff).
	kindDiff
)

type entry struct {
	kind entryKind
	text string
	at   time.Time
	// view / viewW cache the rendered transcript line for width. Glamour is
	// expensive; never re-render unchanged entries on each stream tick.
	view  string
	viewW int
	// plain is true when view is word-wrap only (not glamour); scroll may upgrade.
	plain bool
	// gc means text was stubbed to free memory (cannot re-pretty).
	gc bool
}

type model struct {
	eng   *mow.Engine
	theme theme
	cfg   Config
	md    mdCache
	// mdFaint is the dimmed markdown cache for low-priority progress surfaces
	// (live peer bodies): peers' streaming text must read as "in progress",
	// not as main content competing with the host answer.
	mdFaint mdCache
	vp      viewport.Model
	ta      textarea.Model
	spin    spinner.Model
	width   int
	height  int
	ready   bool

	busy        bool
	quitting    bool
	stream      bool
	showHelp    bool
	showWelcome bool
	modelPick   *modelPicker  // interactive /model overlay
	effortPick  *effortPicker // interactive /effort overlay
	diffView    *diffOverlay  // expanded full-screen diff (compact card stays default)
	// editingPrompt is true while the prompt holds a recalled message awaiting
	// edit (arrow-up on empty input, or /edit). Esc cancels back to a blank
	// prompt; sending clears it.
	editingPrompt bool
	startedAt     time.Time // per-turn elapsed baseline (reset at submit)

	// permMode/autoPower are atomics: the PreTool hook reads them on the
	// engine goroutine while the Update goroutine toggles them (shift+tab,
	// /perm, the "a" answer) — plain fields raced.
	permMode  atomic.Int32 // PermissionMode
	autoPower atomic.Bool

	entries []entry
	// streamRaw is content as received; streamBuf is visible answer after think-strip.
	streamRaw, streamBuf string
	// reasonAPI is OnReasoning deltas; reasonFromTags is <think>… stripped from content.
	// reasonBuf is their concatenation for "is thinking?" only — body is never painted.
	reasonAPI, reasonFromTags string
	reasonBuf                 string
	// streamDirty: answer text changed since last glamour kick.
	streamDirty bool
	// reasonDirty: reasoning changed since last frame rebuild.
	reasonDirty bool
	// streamPaint: paint scheduler running.
	streamPaint bool
	// streamRenderBusy: one in-flight live glamour (answer only).
	streamRenderBusy bool
	// streamGen invalidates stale live glamour frames.
	streamGen uint64
	// streamBody is last glamour-rendered answer (no gutter/caret).
	streamBody string
	// streamBodySrc is streamBuf snapshot for streamBody.
	streamBodySrc string
	// streamFrame is the last assembled live turn (gutter + caret).
	streamFrame string
	// streamFrameW is the width streamFrame was built for.
	streamFrameW int
	// reasonStartedAt is when the first reasoning delta arrived (thinking timer).
	reasonStartedAt time.Time
	// historyCache is the rendered transcript without the live stream frame.
	// Stream paints reuse it so we never re-walk every entry per token.
	historyCache  string
	historyCacheW int
	historyCacheN int // len(entries) when historyCache was built
	// followBottom: when true, stream/transcript refresh pins the viewport to
	// the latest line. Cleared when the user scrolls up; re-armed on submit.
	followBottom bool

	cancel context.CancelFunc
	ingest *streamIngest
	// liveIngest is the active turn's streamIngest, visible to the engine
	// goroutine (EventDelegateChunk). Same batching as OnToken — never drop
	// peer answer deltas on a full toolUI channel.
	liveIngest atomic.Pointer[streamIngest]
	peerIngest *peerDeltaIngest
	// peerLive: any acp_delegate answer is streaming (derived from peerBufs).
	// Late EventDelegate* after the last endPeer must not re-arm the UI.
	peerLive atomic.Bool
	// peerActive counts concurrent acp_delegate tools (parallel peers).
	peerActive atomic.Int32
	// peerAgent is the last EventDelegate* agent name (progress label / fallback).
	peerAgent atomic.Value // string
	// peerBufs holds per-agent live answer text so parallel ACP peers do not
	// interleave into one streamBuf. Key = lowercase agent id. Update-thread only.
	peerBufs map[string]*peerLiveBuf
	// peerOrder is insertion order of peerBufs keys for stable paint.
	peerOrder []string
	// peerExpanded shows peer text live instead of a one-line summary.
	// Off by default: streaming a delegate's reasoning into the transcript
	// repaints on every chunk (flicker) and keeps the region changing under
	// a text selection. Toggled with ctrl+p; the finished reply is committed
	// either way.
	peerExpanded bool
	// mouseOff disables mouse tracking at runtime (select mode), handing the
	// mouse back to the terminal so drag-to-select and copy work again.
	mouseOff bool
	permCh   chan permAskMsg
	toolUICh chan toolUIMsg
	permWait *permAskMsg
	// permArmedAt is when the permission strip became active; y/n/a are ignored
	// until the strip has been shown (permStripShown) and a short arm window
	// elapses — prevents a mid-type "y" from approving an unread shell.
	permArmedAt    time.Time
	permStripShown bool
	// queueTeachShown: one-shot status explaining queue vs /steer.
	queueTeachShown bool
	// selectTeachShown: one-shot status explaining select mode (mouse release).
	// The header chip carries the persistent state after the first toggle.
	selectTeachShown bool
	// lastActivityAt tracks last stream/tool event for stall notes on the band.
	lastActivityAt time.Time
	// lastMouseAt is when the last mouse event was consumed. Up-arrow prompt
	// recall is suppressed briefly after it: terminals can leak wheel-up as an
	// up-arrow escape, and a wheel must never rewrite the prompt.
	lastMouseAt time.Time
	// lastArrowAt is when the last arrow key was consumed. With mouse tracking
	// off (MOW_MOUSE=0) terminals translate wheel events into rapid arrow-key
	// bursts; a burst (<80ms between arrows) is treated as wheel noise and
	// dropped so scroll can never recall a prompt or walk the draft cursor.
	lastArrowAt time.Time
	// pendingRecall holds an up-arrow prompt recall open for a short confirm
	// window (mouse tracking off): a wheel burst's second arrow cancels it
	// before it can fire; a single deliberate press fires after the window.
	pendingRecall   bool
	pendingRecallAt time.Time
	// activityBandOn is the last layout's band visibility (scroll compensate).
	activityBandOn bool
	// ctxPressureBand remembers the highest ctx% warning band already taught.
	ctxPressureBand int
	// hookUnsubs detach the engine hooks on quit — a reused Engine must not
	// keep calling into a dead UI.
	hookUnsubs []func()
	// turnGen invalidates in-flight streamSnapMsg from a previous turn — a
	// snap taken before doneMsg but delivered after the next submit must not
	// bleed old tokens into the new turn.
	turnGen uint64

	// Cumulative provider-reported tokens observed since this TUI process started.
	// A resumed transcript does not restore historical usage, and providers may
	// omit usage; display these as reported activity, never as a billing total.
	tokIn, tokOut int
	// peerTokIn/Out: provider-reported tokens for delegated native mow peers
	// (harness.delegate.usage events) — the header's true-spend total.
	peerTokIn, peerTokOut int
	// goalTokIn/Out track the active goal's last-seen running total so goal
	// events tick the session counter by delta, not double-count.
	goalTokIn, goalTokOut int
	// goalTokID is the goal the token baseline belongs to: State.InputTokens
	// is cumulative across ALL runs of a goal, so the delta must keep the
	// last-seen baseline per goal — same goal reruns add only new tokens, a
	// different goal resets (its cumulative starts near zero).
	goalTokID string
	// queued holds messages typed and sent while a turn was running; drained
	// FIFO when the turn ends.
	queued []string

	// Per-turn tool tally: one transcript line updated in place instead of a
	// line per call (long tool batches were eating the viewport).
	toolTally       []toolCount
	toolLineIdx     int    // entries index of this turn's tally line; -1 = none
	toolCurrent     string // running tool for the live activity band
	toolCurrentArgs string // optional args for smart labels
	// Tool errors fold into the kindTool tally line (⚠ …) — not a separate
	// red row per failure (line_hash misses were flooding the transcript).
	toolErrCount int
	toolErrLast  string // short latest error for the tally suffix

	// lspProblems retains post-edit findings for /lsp during this session.
	lspProblems []lspProblemsEvent

	// Input text colors (swapped when value is a /command).
	inputTextColor lipgloss.Style
	inputPrompt    lipgloss.Style
	slashTextColor lipgloss.Style
	slashPrompt    lipgloss.Style

	// Goal outer-loop (ext/goal): event bus + live header chip.
	goalCh    chan goal.Event
	goalUnsub func()
	goalLive  *goal.State

	// UX: editor vs transcript focus; resize settle gen for debounce.
	focus     focusPane
	resizeGen uint64

	// Transcript search: active term, matching entry indices, and the cursor
	// into them (/search cycles). Entry-based so it survives virtualization.
	searchTerm string
	searchHits []int
	searchIdx  int

	// Virtualized history: heights, line starts, pretty upgrade set, dirty flag.
	entryHeights   []int
	entryLineStart []int
	prettyWant     map[int]bool
	historyDirty   bool
}

const (
	inputMinHeight = 1
	inputMaxHeight = 12 // hard cap; also limited by terminal size in syncInputHeight

	// Below this, View shows a size warning instead of a broken chrome frame.
	minTermWidth  = 40
	minTermHeight = 10
)

// Run starts the Bubble Tea UI until quit.
func Run(eng *mow.Engine) error {
	return RunOpts(Options{Engine: eng})
}

// RunOpts starts the TUI with options.
func RunOpts(opt Options) error {
	if opt.Engine == nil {
		return fmt.Errorf("tui: nil engine")
	}
	// Engine lifecycle uses slog.Info (run start/end, tool start/end). Those
	// lines go to stderr by default and paint over the alt-screen TUI. Keep
	// Warn+ only for this session; headless mow run still gets full Info.
	// Set MOW_LOG=debug to re-enable Info/Debug on stderr while in TUI.
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: tuiLogLevel(),
	})))
	defer slog.SetDefault(prevLog)

	// Resolve light/dark *before* Bubble Tea owns stdin/stdout. OSC 11 probes
	// during the session leak into the input as "]11;rgb:…" and freeze the UI.
	pinTerminalTheme()
	m := newModel(opt.Engine, !opt.DisableStream, opt.AskPermissions)
	// Alt-screen + mouse wheel are declared in View (BT v2 declarative).
	p := tea.NewProgram(m)
	// Bubble Tea turns SIGTERM into a QuitMsg on its event queue. When the
	// loop is stuck (input flood, blocked Update), that quit never runs and
	// kill -TERM looks ignored. SIGHUP is not trapped by tea so the OS kills
	// the process — confusing asymmetry. Watchdog: first signal lets tea try
	// graceful quit; second signal or ~2s timeout force-exits.
	done := make(chan struct{})
	defer close(done)
	go forceExitOnStuckQuit(done, p)
	_, err := p.Run()
	if m.cancel != nil {
		m.cancel()
	}
	if m.goalUnsub != nil {
		m.goalUnsub()
	}
	for _, unsub := range m.hookUnsubs {
		unsub()
	}
	// After the alt-screen is gone (like mow tty): session id + how to resume.
	printSessionExit(opt.Engine)
	// Tear down session-scoped engine resources (e.g. ext/proc background
	// processes are auto-killed on Close) when the TUI exits.
	opt.Engine.Close()
	return err
}

// forceExitOnStuckQuit ensures external kill works when Bubble Tea cannot drain
// its message queue. Tea already Notifies SIGINT/SIGTERM (both get the signal);
// we also take SIGHUP so a frozen TUI dies on the same signals a user tries.
func forceExitOnStuckQuit(done <-chan struct{}, p *tea.Program) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(ch)

	select {
	case <-done:
		return
	case <-ch:
	}
	// Ask tea to stop; if the event loop is healthy this races with tea's own handler.
	if p != nil {
		p.Quit()
	}
	const grace = 2 * time.Second
	select {
	case <-done:
		return
	case <-ch:
		fmt.Fprintln(os.Stderr, "mowi: forced exit (second signal)")
		os.Exit(1)
	case <-time.After(grace):
		fmt.Fprintln(os.Stderr, "mowi: quit timed out, forced exit")
		os.Exit(1)
	}
}

// printSessionExit mirrors mow tty's exit banner: session id and resume
// commands on stderr once the TUI has left the alternate screen.
func printSessionExit(eng *mow.Engine) {
	if eng == nil {
		return
	}
	sid := strings.TrimSpace(eng.SessionID())
	if sid == "" {
		return // --no-session or session never created
	}
	fmt.Fprintf(os.Stderr, "session=%s\n", sid)
	fmt.Fprintf(os.Stderr, "resume: mowi --session %s\n", sid)
	fmt.Fprintf(os.Stderr, "        mowi --continue\n")
}

// tuiLogLevel is Warn by default so slog.Info lifecycle lines do not corrupt
// the UI. MOW_LOG=debug|info lowers the threshold when debugging.
// reducedMotion stills decorative animation (the spinner) when MOW_NO_ANIM is
// set — an accessibility pairing with NO_COLOR. The elapsed clock still ticks
// (it is information, not decoration).
func reducedMotion() bool {
	v := strings.TrimSpace(os.Getenv("MOW_NO_ANIM"))
	return v != "" && v != "0"
}

func tuiLogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MOW_LOG"))) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning", "":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

func newModel(eng *mow.Engine, stream, ask bool) *model {
	// Pin light/dark once (no OSC mid-session). Named themes may force dark (monokai).
	termDark := pinTerminalTheme()
	tuiCfg := LoadConfig(eng)
	th := newThemeFrom(tuiCfg.Theme, termDark)

	ta := textarea.New()
	// Grow/shrink with content (hard newlines + soft wrap). MaxHeight is
	// re-clamped to terminal room in applyInputHeightCap.
	ta.Placeholder = ""
	ta.Prompt = tuiCfg.PromptPrefix()
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = inputMinHeight
	ta.MaxHeight = inputMaxHeight
	ta.SetHeight(inputMinHeight)
	// Input colors follow the selected theme (no AdaptiveColor probes).
	// Regular weight — the message you type should read as plain text, not bold.
	inputText := th.Text                 // plain fg
	inputPrompt := th.Accent.Bold(false) // accent color, regular
	slashText := th.SlashCmd.Bold(false)
	slashPrompt := th.SlashCmd.Bold(false)

	st := textarea.DefaultStyles(termDark)
	st.Focused.Base = inputText
	st.Focused.CursorLine = lipgloss.NewStyle() // no full-line highlight
	st.Focused.Placeholder = th.Muted
	st.Focused.Prompt = inputPrompt
	st.Focused.Text = inputText
	st.Blurred.Base = th.Muted
	st.Blurred.CursorLine = lipgloss.NewStyle()
	st.Blurred.Placeholder = th.Muted
	st.Blurred.Prompt = th.Muted
	st.Cursor.Color = th.Accent.GetForeground()
	st.Cursor.Blink = true
	ta.SetStyles(st)
	keys := tuiCfg.Keys // already Resolve()'d in LoadConfig
	km := textarea.DefaultKeyMap()
	// Newline from config (default ctrl+j); send is handled in Update, not InsertNewline.
	nl := keys.All(keys.Newline)
	if len(nl) == 0 {
		nl = []string{"ctrl+j"}
	}
	km.InsertNewline = key.NewBinding(key.WithKeys(nl...), key.WithHelp(keys.Primary(keys.Newline), "newline"))
	ta.KeyMap = km
	_ = ta.Focus() // arms cursor blink

	sp := spinner.New()
	// MiniDot is the polished bubbles spinner — quiet but clearly animated.
	sp.Spinner = spinner.MiniDot
	sp.Style = th.Accent

	perm := PermAuto
	if ask {
		perm = PermAsk
	}
	// Larger buffer: acp_delegate can emit many peer progress/chunk events.
	toolUI := make(chan toolUIMsg, 128)
	m := &model{
		eng:            eng,
		theme:          th,
		cfg:            tuiCfg,
		md:             newMDCacheFromTheme(th),
		mdFaint:        newMDCacheFaintFromTheme(th),
		vp:             viewport.New(),
		ta:             ta,
		spin:           sp,
		stream:         stream,
		showWelcome:    tuiCfg.ShowWelcome(),
		followBottom:   true,
		permCh:         make(chan permAskMsg, 8),
		toolUICh:       toolUI,
		peerIngest:     newPeerDeltaIngest(),
		inputTextColor: inputText,
		inputPrompt:    inputPrompt,
		slashTextColor: slashText,
		slashPrompt:    slashPrompt,
	}
	m.setPerm(perm)
	m.resetToolTally() // indices are -1-based sentinels, not zero values

	// Prompt indicator on the FIRST line only. The head (idle "❯ " or the busy
	// spinner/timer) lives in ta.Prompt; continuation lines stay blank so a
	// multi-line message aligns under one prompt. The reservation must match the
	// prompt's own width — a wider reservation pads every row and pushes the
	// draft text away from the left edge.
	promptW := lipgloss.Width(tuiCfg.PromptPrefix())
	m.ta.SetPromptFunc(promptW, func(pi textarea.PromptInfo) string {
		if pi.LineNumber == 0 {
			return xansi.Truncate(m.ta.Prompt, promptW, "")
		}
		return ""
	})

	// Intermediate turns: the model narrates between tool batches; commit that
	// prose as transcript entries at each boundary so it neither welds across
	// turns nor disappears when the final answer lands.
	unsubTurn := eng.AddAfterTurn(func(ctx context.Context, e mow.AfterTurnEvent) {
		if !e.HasToolCalls || strings.TrimSpace(e.AssistantText) == "" {
			return // final turn text is committed by doneMsg
		}
		select {
		case m.toolUICh <- toolUIMsg{turnText: e.AssistantText}:
		default:
			// drop if UI is behind — text still reaches history via mow
		}
	})

	// acp_delegate peer activity (mow EventDelegate*).
	// Answer chunks: streamIngest (batched, never drop). Progress: throttled label.
	// After the last peer ends, drop late progress/chunks so the UI is not stuck
	// "still doing" peer work while the host model is synthesizing.
	var lastPeerProgress atomic.Int64 // unix nano; throttle spinner thrash
	unsubEv := eng.AddOnEvent(func(ev mow.Event) {
		switch ev.Type {
		case mow.EventSteer:
			// The in-flight LLM call was interrupted for a mid-turn steer; the
			// answer will be reissued with the steer appended. Reset the live
			// stream so the fresh answer does not weld onto the partial.
			if m.busy {
				m.clearLiveStream()
				// clearLiveStream only clears the paint buffers. The shared
				// streamIngest may still hold a partial tail from the
				// interrupted call, and a streamSnapMsg taken before the steer
				// can be delivered after it — either would re-paint the
				// cleared partial, then the fresh answer welds onto it. Bump
				// turnGen so every in-flight/stale snap is dropped by the gen
				// guard; the reissued answer then starts clean from the
				// re-armed poll (the interrupted tail is dropped in the same
				// stale batch — the loop never committed it to history).
				m.turnGen++
				m.paintLiveStream()
			}
		case mow.EventCompact:
			// ContextTokens already refreshed in the engine; ping Update so the
			// header ctx% chip drops without waiting for the next doneMsg.
			select {
			case m.toolUICh <- toolUIMsg{compactDone: true}:
			default:
			}
		case mow.EventDelegateUsage:
			// Peer spend (native mow peers report usage; external agents may
			// omit it → zeros). Forward to the Update goroutine (counters are
			// read by renderHeader on that goroutine — never mutate here).
			if ev.InputTokens > 0 || ev.OutputTokens > 0 {
				select {
				case m.toolUICh <- toolUIMsg{peerUsage: struct {
					in, out int
				}{ev.InputTokens, ev.OutputTokens}}:
				default:
				}
			}
		case mow.EventDelegateProgress:
			if !m.peerLive.Load() {
				return // peer window closed — ignore stragglers
			}
			agent := strings.TrimSpace(ev.Agent)
			if agent == "" {
				agent = "peer"
			}
			m.peerAgent.Store(agent)
			tool := strings.TrimSpace(ev.Tool)
			delta := strings.TrimSpace(ev.Delta)
			var line string
			switch {
			case tool == "tool" || tool == "thought":
				line = delta
				if line == "" {
					line = tool
				}
			case tool != "" && delta != "":
				line = tool + " " + delta
			case tool != "":
				line = tool
			default:
				line = delta
			}
			if line == "" {
				return
			}
			// Throttle activity-band label updates so spinner doesn't thrash.
			now := time.Now().UnixNano()
			prev := lastPeerProgress.Load()
			if prev != 0 && now-prev < int64(120*time.Millisecond) {
				return
			}
			lastPeerProgress.Store(now)
			label := agent + ": " + line
			select {
			case m.toolUICh <- toolUIMsg{name: label, start: true, line: label}:
			default:
			}
		case mow.EventLSPDiagnostics:
			if ev.Count <= 0 {
				return
			}
			problems := &lspProblemsEvent{
				path:        ev.Path,
				count:       ev.Count,
				diagnostics: append([]mow.Diagnostic(nil), ev.Diagnostics...),
			}
			select {
			case m.toolUICh <- toolUIMsg{lsp: problems}:
			default:
			}
		case mow.EventDelegateChunk:
			if ev.Delta == "" {
				return
			}
			// Peer answer chunks bypass the bounded Bubble Tea channel. They are
			// drained on the UI heartbeat and before endPeer commits the reply.
			if !m.peerLive.Load() {
				return
			}
			agent := strings.TrimSpace(ev.Agent)
			if agent != "" {
				m.peerAgent.Store(agent)
			}
			m.peerIngest.push(agent, ev.Delta)
		}
	})

	unsubPre := eng.AddPreTool(func(ctx context.Context, e mow.PreToolEvent) (mow.PreToolDecision, error) {
		// Live progress: mark the tool as running (drop if the UI is behind).
		// acp_delegate: clear host stream so peer answer does not weld onto it.
		msg := toolUIMsg{name: e.Name, start: true, args: permPreview(e.Name, e.Args)}
		if strings.EqualFold(e.Name, "acp_delegate") {
			// Arm the peer window before the tool runs. Delegate chunks arrive on
			// the engine goroutine and can precede Bubble Tea consuming clearStream;
			// gating only on the UI message would drop the beginning of a reply.
			m.peerActive.Add(1)
			m.peerLive.Store(true)
			msg.clearStream = true
			msg.peerArmed = true
			var a struct {
				Agent string `json:"agent"`
			}
			if json.Unmarshal(e.Args, &a) == nil {
				if ag := strings.TrimSpace(a.Agent); ag != "" {
					m.peerAgent.Store(ag)
					msg.name = ag + ": acp_delegate"
				}
			}
		}
		select {
		case m.toolUICh <- msg:
		default:
		}
		if !isPowerTool(e.Name) {
			return mow.PreToolDecision{}, nil
		}
		if m.perm() == PermAuto || m.autoPower.Load() {
			return mow.PreToolDecision{}, nil
		}
		resp := make(chan error, 1)
		req := permAskMsg{name: e.Name, args: permPreview(e.Name, e.Args), resp: resp}
		select {
		case m.permCh <- req:
		case <-ctx.Done():
			return mow.PreToolDecision{}, ctx.Err()
		}
		select {
		case err := <-resp:
			if err != nil {
				// Deny as tool result (model continues); ctx cancel hard-aborts above.
				return mow.PreToolDecision{Deny: true, Message: err.Error()}, nil
			}
			return mow.PreToolDecision{}, nil
		case <-ctx.Done():
			return mow.PreToolDecision{}, ctx.Err()
		}
	})

	// Surface every tool run into the transcript: with --allow-shell in auto
	// mode, commands used to execute with zero trace. write/edit add a diff.
	unsubPost := eng.AddPostTool(func(ctx context.Context, e mow.PostToolEvent) (mow.PostToolDecision, error) {
		if e.Denied {
			// The permission flow already logged the denial.
			return mow.PostToolDecision{}, nil
		}
		name := strings.ToLower(e.Name)
		sum := name
		if d := e.Duration; d > 0 {
			sum += " · " + formatElapsed(d)
		}
		var diff string
		if e.ExecErr != nil {
			sum += " · error · " + truncate(e.ExecErr.Error(), 120)
		} else if name == "write" || name == "edit" {
			diff = e.Result
		}
		msg := toolUIMsg{name: e.Name, line: sum, text: diff, isErr: e.ExecErr != nil}
		if name == "acp_delegate" {
			// Must deliver endPeer: otherwise peerLive stays armed and the UI
			// looks like peers are still running after they finished.
			msg.endPeer = true
			var a struct {
				Agent string `json:"agent"`
			}
			if json.Unmarshal(e.Args, &a) == nil {
				msg.peerAgent = strings.TrimSpace(a.Agent)
			}
			if msg.peerAgent == "" {
				if v, ok := m.peerAgent.Load().(string); ok {
					msg.peerAgent = strings.TrimSpace(v)
				}
			}
			select {
			case m.toolUICh <- msg:
			case <-ctx.Done():
			}
			return mow.PostToolDecision{}, nil
		}
		select {
		case m.toolUICh <- msg:
		default:
			// drop if UI is behind; model still has the full result
		}
		return mow.PostToolDecision{}, nil
	})

	m.hookUnsubs = []func(){unsubTurn, unsubEv, unsubPre, unsubPost}

	// Resume: seed viewport from session transcript (engine already loaded prior).
	seedTranscript(m, eng)
	// Goal event bus (headless pack may also emit; TUI only displays).
	m.initGoalBus()

	return m
}

// seedTranscript paints prior user/assistant turns on resume so the TUI is not blank.
func seedTranscript(m *model, eng *mow.Engine) {
	if eng == nil {
		return
	}
	turns := eng.Transcript()
	if len(turns) == 0 {
		return
	}
	for _, msg := range turns {
		switch strings.ToLower(msg.Role) {
		case "user":
			if t := strings.TrimSpace(msg.Content); t != "" {
				m.addAt(kindUser, t, time.Time{}) // original time unknown — no stamp
			}
		case "assistant":
			if t := strings.TrimSpace(msg.Content); t != "" {
				m.addAt(kindAssistant, t, time.Time{})
			}
		}
	}
	if len(m.entries) > 0 {
		m.showWelcome = false
		m.followBottom = true
		n := 0
		for _, e := range m.entries {
			if e.kind == kindUser || e.kind == kindAssistant {
				n++
			}
		}
		sid := strings.TrimSpace(eng.SessionID())
		line := fmt.Sprintf("resumed · %d turns", n)
		if sid != "" {
			line = fmt.Sprintf("resumed · session %s · %d turns", short(sid, 12), n)
		}
		m.add(kindStatus, line)
	}
}

func (m *model) Init() tea.Cmd {
	// Do not start spinner here — idle has no spinner; submit starts heartbeat.
	return tea.Batch(textarea.Blink, m.pollPerm(), m.pollToolUI(), m.pollGoal())
}

func (m *model) pollPerm() tea.Cmd {
	return func() tea.Msg { return <-m.permCh }
}

func (m *model) pollToolUI() tea.Cmd {
	return func() tea.Msg { return <-m.toolUICh }
}
