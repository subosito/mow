// Package mowi is the Bubble Tea UI for the mow headless harness
// ("mow with interface"). Import path: github.com/subosito/mow/packs/mowi
//
// Config section: extensions.tui (shared MOW_HOME with mow).
// It does not implement the agent loop; all work goes through mow.Engine.
package mowi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

func statusBits(eng *mow.Engine, perm PermissionMode, stream bool) string {
	ws := filepath.Base(eng.Workspace())
	parts := []string{eng.Model(), ws, "perm " + perm.String()}
	if eng.AllowWrite() {
		parts = append(parts, "write")
	}
	if eng.AllowShell() {
		parts = append(parts, "shell")
	}
	if stream {
		parts = append(parts, "stream")
	}
	if sid := eng.SessionID(); sid != "" {
		parts = append(parts, sid)
	}
	return strings.Join(parts, " · ")
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

func (m *model) isSlashInput() bool {
	v := strings.TrimLeft(m.ta.Value(), " \t")
	return strings.HasPrefix(v, "/")
}

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
			// wheel falls back to the arrow-burst guard while off.
			m.mouseOff = !m.mouseOff
			if m.mouseOff {
				m.add(kindStatus, "select mode on — mouse released, drag to select ("+
					m.cfg.Keys.Primary(m.cfg.Keys.SelectMode)+" to resume scroll)")
			} else {
				m.add(kindStatus, "select mode off — wheel scrolls the transcript")
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
			if !m.busy && m.modelPick == nil {
				m.showHelp = true
			}
			return m, nil
		case keyStr == "?" && !m.busy && m.modelPick == nil && strings.TrimSpace(m.ta.Value()) == "":
			// Soft help when input empty (not configurable — avoids stealing typing).
			m.showHelp = true
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

	case reviewDoneMsg:
		return m, m.applyReviewDone(msg)

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
			switch {
			case errors.Is(msg.err, context.Canceled):
				m.add(kindStatus, "cancelled")
			case errors.Is(msg.err, context.DeadlineExceeded):
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
	canType := m.permWait == nil && !m.showHelp && m.modelPick == nil && m.effortPick == nil && m.focus == focusEditor
	// Mouse belongs exclusively to the transcript viewport. Passing wheel or
	// click messages through textarea.Update first can move its internal view/
	// cursor even though the draft text is unchanged.
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
	// Mouse: wheel scrolls the transcript only — it must never touch the draft
	// or recall a prompt (arrow keys own prompt recall). Motion/click are
	// dropped so they never flood Update.
	if m.mouseOn() {
		switch msg.(type) {
		case tea.MouseWheelMsg:
			m.lastMouseAt = time.Now() // wheel activity: arm the KeyUp grace window
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

// queueDraft parks the current draft to auto-send when the running turn ends.
// Slash commands are not queued — they act on live UI state, so they run now.
func (m *model) queueDraft() tea.Cmd {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		m.ta.Reset()
		return m.handleSlash(text)
	}
	m.ta.Reset()
	m.queued = append(m.queued, text)
	// Queue visibility lives on the activity band (ephemeral). Do not paste
	// draft preview into the transcript — cancel would leave document fiction.
	if !m.queueTeachShown {
		m.queueTeachShown = true
		m.add(kindStatus, "queued · will send after this turn ( /steer to inject now )")
	}
	m.syncInputChrome()
	m.layout()
	m.refreshVP()
	return nil
}

// dropQueue discards queued messages (turn cancelled — follow-ups no longer apply).
func (m *model) dropQueue() {
	if len(m.queued) == 0 {
		return
	}
	n := len(m.queued)
	m.queued = nil
	m.add(kindStatus, fmt.Sprintf("cancelled · dropped %d queued message(s)", n))
}

// dequeue pops the next queued message and submits it (called at turn end).
func (m *model) dequeue() (tea.Model, tea.Cmd) {
	if len(m.queued) == 0 || m.busy {
		return m, nil
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	m.ta.SetValue(next)
	return m.submit()
}

func (m *model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return m, nil
	}
	m.ta.Reset()
	// /btw <question>: an aside answered against the current conversation but
	// NOT added to context (mow runs it Ephemeral) — handled before the generic
	// slash dispatch so it actually runs a turn.
	if arg, ok := parseBtw(text); ok {
		if arg == "" {
			m.showWelcome = false
			m.add(kindStatus, "btw — usage: /btw <question>  (aside, not added to context)")
			m.refreshVP()
			return m, nil
		}
		return m.startTurn(arg, true)
	}
	if strings.HasPrefix(text, "/") {
		return m, m.handleSlash(text)
	}
	return m.startTurn(text, false)
}

// parseBtw detects the /btw aside command and returns its argument.
func parseBtw(text string) (arg string, ok bool) {
	if text == "/btw" {
		return "", true
	}
	if r, has := strings.CutPrefix(text, "/btw "); has {
		return strings.TrimSpace(r), true
	}
	return "", false
}

// parseSteer detects the /steer command and returns its guidance argument.
func parseSteer(text string) (arg string, ok bool) {
	if text == "/steer" {
		return "", true
	}
	if r, has := strings.CutPrefix(text, "/steer "); has {
		return strings.TrimSpace(r), true
	}
	return "", false
}

// doSteer injects guidance into the running turn (mow appends it at the next
// turn boundary). Marked in the transcript so it's clear it's steering, not a
// normal message.
func (m *model) doSteer(text string) tea.Cmd {
	if text == "" {
		m.add(kindStatus, "steer · usage: /steer <guidance>  (while a turn runs)")
		m.refreshVP()
		return nil
	}
	if !m.busy {
		m.add(kindStatus, "steer · no turn running — just send your message")
		m.refreshVP()
		return nil
	}
	m.eng.Steer(text)
	m.add(kindStatus, "steer "+glyphArrow+" "+truncate(text, 80))
	m.refreshVP()
	return nil
}

func (m *model) handleSlash(cmd string) tea.Cmd {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil
	}
	if parts[0] != "/help" && parts[0] != "/?" {
		m.showWelcome = false
	}
	switch parts[0] {
	case "/help", "/?":
		m.showHelp = true
	case "/clear":
		m.clearTranscript()
	case "/quit", "/exit":
		m.quitting = true
		return tea.Quit
	case "/perm":
		if len(parts) > 1 {
			switch parts[1] {
			case "ask":
				m.setPerm(PermAsk)
				m.autoPower.Store(false)
			case "auto":
				m.setPerm(PermAuto)
			default:
				m.add(kindError, "usage: /perm [auto|ask]")
				m.refreshVP()
				return nil
			}
			m.add(kindStatus, "perm "+glyphArrow+" "+m.perm().String())
		} else {
			m.togglePerm()
			return nil
		}
	case "/compact":
		// Manual compaction: rewrites the stored transcript (the context the
		// next prompt resumes with) using the tiered snip→drop machinery.
		// During a busy turn the loop owns the live messages, so compact
		// applies when the turn ends — the status says so honestly.
		if m.busy {
			m.add(kindStatus, "compact · applies when the turn finishes (stored history)")
			m.refreshVP()
			return nil
		}
		rep, err := m.eng.Compact(0)
		if err != nil {
			m.add(kindError, "compact · "+err.Error())
			m.refreshVP()
			return nil
		}
		switch {
		case rep.CharsSaved <= 0:
			m.add(kindStatus, "compact · nothing to trim")
		default:
			m.add(kindStatus, fmt.Sprintf("compact · %s saved %s · %d→%d msgs",
				rep.Layer, formatTokens(rep.CharsSaved), rep.MessagesBefore, rep.MessagesAfter))
			// Engine.Compact refreshes ContextTokens(); clear the one-shot
			// pressure band so a later climb can teach again, and relayout so
			// the header ctx% chip drops without waiting for another turn.
			m.ctxPressureBand = 0
		}
		m.layout()
		m.refreshVP()
		return nil
	case "/status":
		line := statusBits(m.eng, m.perm(), m.stream)
		if m.goalLive != nil {
			line += " · " + goalHeaderChip(m.goalLive)
		}
		if usage := m.reportedUsageStatus(); usage != "" {
			line += "\n" + usage
		}
		m.add(kindStatus, line)
		m.refreshVP()
		return nil
	case "/goal":
		return m.handleGoalSlash(parts)
	case "/lsp":
		m.showLSPProblems()
		m.refreshVP()
		return nil
	case "/review", "/sec":
		return m.handleReviewSlash(parts)
	case "/model":
		filter := ""
		if len(parts) > 1 {
			filter = strings.Join(parts[1:], " ")
		}
		return m.cmdModel(filter)
	case "/effort":
		// Bare /effort opens the picker (mirrors /model); an argument sets directly.
		if len(parts) > 1 {
			return m.cmdEffort(strings.Join(parts[1:], " "))
		}
		if m.effortPick == nil {
			m.openEffortPicker()
		} else {
			m.closeEffortPicker()
		}
		m.refreshVP()
		return nil
	case "/copy", "/yank":
		return m.copyLastAnswer()
	case "/sessions":
		return m.listSessions()
	case "/search", "/find":
		m.doSearch(strings.TrimSpace(strings.TrimPrefix(cmd, parts[0])))
		return nil
	case "/retry", "/regen":
		return m.retryLast()
	case "/edit":
		return m.editLast()
	case "/steer":
		// Idle path (busy path is intercepted at the Send key). No turn running.
		return m.doSteer(strings.TrimSpace(strings.TrimPrefix(cmd, parts[0])))
	default:
		m.add(kindError, "unknown "+parts[0]+" — /help")
	}
	m.refreshVP()
	return nil
}

// dropLastTurnEntries removes the transcript entries of the most recent turn
// (from the last user prompt to the end), so a retry/edit replaces it rather
// than stacking a second copy. Virtualization caches are rebuilt on next paint.
func (m *model) dropLastTurnEntries() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == kindUser {
			m.entries = m.entries[:i]
			break
		}
	}
	m.entryHeights = nil
	m.entryLineStart = nil
	m.prettyWant = nil
	m.historyDirty = true
	m.toolLineIdx = -1
	m.invalidateHistoryCache()
}

// retryLast rewinds the last exchange and regenerates the answer for the same
// prompt (a fresh sample without the discarded answer in context).
func (m *model) retryLast() tea.Cmd {
	if m.busy {
		m.add(kindStatus, "retry · wait for the current turn to finish")
		m.refreshVP()
		return nil
	}
	last, ok := m.eng.Rewind()
	if !ok || strings.TrimSpace(last) == "" {
		m.add(kindStatus, "retry · nothing to retry")
		m.refreshVP()
		return nil
	}
	m.dropLastTurnEntries()
	_, cmd := m.startTurn(last, false)
	return cmd
}

// editLast rewinds the last exchange and loads that prompt into the input for
// editing; sending replaces the removed turn.
func (m *model) editLast() tea.Cmd {
	if m.busy {
		m.add(kindStatus, "edit · wait for the current turn to finish")
		m.refreshVP()
		return nil
	}
	last, ok := m.eng.Rewind()
	if !ok || strings.TrimSpace(last) == "" {
		m.add(kindStatus, "edit · nothing to edit")
		m.refreshVP()
		return nil
	}
	m.dropLastTurnEntries()
	m.editingPrompt = true
	m.add(kindStatus, "editing last message — change it and press enter · esc cancels")
	m.ta.SetValue(last)
	m.syncInputHeight()
	m.syncInputChrome()
	m.layout()
	m.refreshVP()
	return nil
}

// doSearch finds transcript entries containing term and scrolls to matches.
// A new term jumps to the first match; a bare /search cycles to the next match
// of the active term (wrapping). No new keybindings — cycle via /search.
func (m *model) doSearch(term string) {
	if term == "" {
		if m.searchTerm == "" || len(m.searchHits) == 0 {
			m.add(kindStatus, "search · usage: /search <term>  (repeat /search to cycle)")
			m.refreshVP()
			return
		}
		m.searchIdx = (m.searchIdx + 1) % len(m.searchHits)
		m.scrollToEntry(m.searchHits[m.searchIdx])
		m.add(kindStatus, fmt.Sprintf("search %q · %d/%d", m.searchTerm, m.searchIdx+1, len(m.searchHits)))
		m.refreshVP()
		return
	}
	low := strings.ToLower(term)
	m.searchHits = m.searchHits[:0]
	for i, e := range m.entries {
		if e.kind == kindUser || e.kind == kindAssistant {
			if strings.Contains(strings.ToLower(e.text), low) {
				m.searchHits = append(m.searchHits, i)
			}
		}
	}
	m.searchTerm = term
	m.searchIdx = 0
	if len(m.searchHits) == 0 {
		m.add(kindStatus, fmt.Sprintf("search %q · no matches", term))
		m.refreshVP()
		return
	}
	m.add(kindStatus, fmt.Sprintf("search %q · %d match(es), showing 1 (/search to cycle)", term, len(m.searchHits)))
	m.refreshVP() // rebuilds entryLineStart before we scroll
	m.scrollToEntry(m.searchHits[0])
}

// scrollToEntry pins the viewport to an entry's first line (clears follow so the
// stream/refresh does not yank us back to the bottom).
func (m *model) scrollToEntry(idx int) {
	if idx < 0 || idx >= len(m.entryLineStart) {
		return
	}
	m.followBottom = false
	m.vp.SetYOffset(m.entryLineStart[idx])
}

// copyLastAnswer yanks the most recent assistant answer to the system
// clipboard via OSC52 (works over SSH; terminal must permit clipboard writes).
func (m *model) copyLastAnswer() tea.Cmd {
	text := ""
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == kindAssistant && !m.entries[i].gc {
			text = m.entries[i].text
			break
		}
	}
	if strings.TrimSpace(text) == "" {
		m.add(kindStatus, "copy · no answer to copy")
		m.refreshVP()
		return nil
	}
	m.add(kindStatus, fmt.Sprintf("copied · %d chars to clipboard", len(text)))
	m.refreshVP()
	return tea.SetClipboard(text)
}

// listSessions shows resumable sessions for this workspace. mowi holds one
// Engine, so switching is out-of-process — the list surfaces ids + previews
// and the relaunch command.
func (m *model) listSessions() tea.Cmd {
	infos, err := m.eng.Sessions()
	if err != nil {
		m.add(kindError, "sessions: "+err.Error())
		m.refreshVP()
		return nil
	}
	if len(infos) == 0 {
		m.add(kindStatus, "sessions · (none)")
		m.refreshVP()
		return nil
	}
	var b strings.Builder
	b.WriteString("sessions (newest first)\n")
	cur := m.eng.SessionID()
	const maxShow = 20
	for i, s := range infos {
		if i >= maxShow {
			fmt.Fprintf(&b, "… %d more\n", len(infos)-maxShow)
			break
		}
		mark := "  "
		if s.ID == cur {
			mark = "• "
		}
		when := formatTurnTime(s.Updated, time.Now())
		fmt.Fprintf(&b, "%s%-16s  %-12s  %s\n", mark, s.ID, when, short(s.Preview, 44))
	}
	b.WriteString("resume: relaunch with --session <id> (or --continue for the latest)")
	m.add(kindStatus, strings.TrimRight(b.String(), "\n"))
	m.refreshVP()
	return nil
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

func (m *model) applyModelList(msg modelListMsg) {
	if msg.setTo != "" {
		line := "model " + glyphArrow + " " + msg.setTo
		if msg.setWire != "" {
			line += " · " + msg.setWire
		}
		m.add(kindStatus, line)
		m.closeModelPicker()
		return
	}
	if msg.err != nil {
		m.add(kindError, "model: "+msg.err.Error())
		if msg.current != "" {
			m.add(kindStatus, "current model · "+msg.current)
		}
		// Still open picker when we have a catalog to choose from.
		if msg.openPicker && len(msg.models) > 0 {
			m.openModelPicker(msg.models, msg.current, msg.filter)
		}
		return
	}
	if len(msg.models) == 0 {
		m.add(kindStatus, "models · (empty catalog)")
		m.closeModelPicker()
		return
	}
	// Interactive picker (default for /model and multi-match filters).
	if msg.openPicker || msg.filter == "" {
		m.openModelPicker(msg.models, msg.current, msg.filter)
		return
	}
	// Fallback: dump list into transcript (should be rare).
	var b strings.Builder
	b.WriteString("models")
	if msg.current != "" {
		b.WriteString(" · current " + msg.current)
	}
	b.WriteString("\n")
	for _, info := range msg.models {
		line := "  " + info.ID
		if info.Wire != "" {
			line += "  [" + info.Wire + "]"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("switch: /model <id>")
	m.add(kindStatus, strings.TrimRight(b.String(), "\n"))
}

// cmdEffort cycles or sets reasoning effort (none|low|medium|high).
func (m *model) cmdEffort(filter string) tea.Cmd {
	eng := m.eng
	if eng == nil {
		return func() tea.Msg { return effortMsg{err: fmt.Errorf("no engine")} }
	}
	efforts := eng.Efforts()
	cur := eng.Effort()
	var cycle []string
	if len(efforts) > 0 {
		cycle = append([]string(nil), efforts...)
	} else {
		cycle = []string{"none", "low", "medium", "high"}
	}
	if filter == "" {
		next := ""
		for i, e := range cycle {
			if strings.EqualFold(e, cur) {
				next = cycle[(i+1)%len(cycle)]
				break
			}
		}
		if next == "" && len(cycle) > 0 {
			next = cycle[0]
		}
		if next == "" {
			return func() tea.Msg { return effortMsg{current: cur} }
		}
		if err := eng.SetEffort(next); err != nil {
			return func() tea.Msg { return effortMsg{err: err} }
		}
		return func() tea.Msg { return effortMsg{setTo: next, current: eng.Effort()} }
	}
	target := strings.ToLower(strings.TrimSpace(filter))
	found := ""
	for _, e := range cycle {
		if strings.EqualFold(e, target) {
			found = e
			break
		}
	}
	if found == "" {
		return func() tea.Msg { return effortMsg{current: cur, err: fmt.Errorf("unknown effort %q", filter)} }
	}
	if err := eng.SetEffort(found); err != nil {
		return func() tea.Msg { return effortMsg{err: err} }
	}
	return func() tea.Msg { return effortMsg{setTo: found, current: eng.Effort()} }
}

type effortMsg struct {
	setTo   string
	current string
	err     error
}
