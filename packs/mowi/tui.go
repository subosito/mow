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
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
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

// toolCount is one name's call count in the per-turn tool tally.
type toolCount struct {
	name  string
	count int
}

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

// peerLiveBuf is one in-flight acp_delegate answer, keyed by agent name.
type peerLiveBuf struct {
	agent    string
	buf      string   // bounded display buffer; full is committed
	full     string   // complete sanitized answer, never trimmed for live paint
	body     string   // last markdown-rendered answer body
	bodySrc  string   // source snapshot for body
	dirty    bool     // answer needs a markdown render
	progress []string // bounded recent thought/tool progress, never committed as the answer
}

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

type (
	doneMsg struct {
		text  string
		usage mow.Usage
		err   error
	}
	// recallConfirmMsg fires after the up-arrow confirm window when mouse
	// tracking is off: a wheel burst's second arrow cancels the held recall
	// before this tick; a single deliberate press runs editLast.
	recallConfirmMsg struct{}
	// streamSnapMsg is a batched content/reasoning drain from streamIngest.
	streamSnapMsg struct {
		gen       uint64 // turnGen at arm time; stale snaps are dropped
		content   string
		reasoning string
		finished  bool // ingest finished; stop polling after applying
	}
	// deltaMsg / reasoningMsg kept for tests (single-piece updates).
	deltaMsg     string
	reasoningMsg string
	permAskMsg   struct {
		name string
		args string
		resp chan error
	}
	// streamPaintMsg throttles live frame rebuilds / glamour kicks while busy.
	streamPaintMsg struct{}
	// streamRenderedMsg is async glamour for live answer content (not reasoning).
	streamRenderedMsg struct {
		gen     uint64
		width   int
		src     string
		body    string
		peerKey string // non-empty → peerBufs[peerKey]
	}
	// entryPrettyMsg is async glamour for a finished assistant entry (never on Update).
	entryPrettyMsg struct {
		idx   int
		width int
		src   string
		body  string
	}
	// toolUIMsg surfaces tool activity: start marks a tool beginning (live
	// indicator only); end events update the per-turn tally line in place —
	// one transcript line per turn, not one per call. Diffs for write/edit.
	// streamDelta is peer acp_delegate answer text (EventDelegateChunk).
	// Peer answer chunks are batched through peerIngest (never dropped on a
	// full toolUI channel); streamDelta is for tests and the rare no-ingest
	// fallback.
	toolUIMsg struct {
		name  string
		start bool   // tool began; update the live indicator, nothing else
		line  string // "name · 0.4s" summary (empty = no line)
		text  string // write/edit diff body
		args  string // optional raw/preview args for activity-band labels
		isErr bool
		// compactDone: engine refreshed ContextTokens after loop.compact;
		// clear pressure band and redraw header ctx% without a new turn.
		compactDone bool
		// turnText commits an intermediate turn's assistant prose at the
		// tool boundary — without this the model's between-tools narration
		// streams live, welds across turns, then vanishes at run end.
		turnText string
		// streamDelta is peer answer text (EventDelegateChunk).
		streamDelta string
		// peerAgent routes peer chunks/end to a per-agent live buffer.
		peerAgent string
		// clearStream opens a peer live slot (acp_delegate start); host stream wiped once.
		clearStream bool
		// peerArmed means the PreTool hook armed the peer before enqueueing this
		// UI message; tests and synthetic messages leave it false.
		peerArmed bool
		// endPeer: acp_delegate finished — commit that peer's live text only.
		endPeer bool
		// lsp carries post-edit diagnostics from the engine event hook.
		lsp *lspProblemsEvent
		// peerUsage carries one delegated peer's provider-reported tokens
		// (harness.delegate.usage) to the Update goroutine for accumulation.
		peerUsage struct {
			in, out int
		}
	}
	// busyHeartbeatMsg drives spinner + elapsed while busy. Independent of
	// bubbles' internal tag chain (which can stop and leave the spinner frozen).
	busyHeartbeatMsg struct{}
	// modelListMsg is the result of /model list (or filtered set attempt).
	modelListMsg struct {
		models     []mow.ModelInfo
		current    string
		filter     string
		setTo      string // non-empty when a unique match was applied
		setWire    string // effective wire after set (catalog or default)
		openPicker bool   // open interactive picker with models
		err        error
	}
)

type lspProblemsEvent struct {
	path        string
	count       int
	diagnostics []mow.Diagnostic
}

const (
	maxLSPProblemEntries = 3
	maxLSPProblemPaths   = 10
	maxLSPRecentBatches  = 5
	maxLSPRecentLines    = 40
)

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

// spinnerView is the busy spinner, or a static glyph under reduced peer-bion.
func (m *model) spinnerView() string {
	if reducedMotion() {
		return m.theme.Accent.Render(glyphBrand)
	}
	return m.spin.View()
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

// perm / setPerm are the only access paths for permMode (atomic — see field).
func (m *model) perm() PermissionMode {
	return PermissionMode(m.permMode.Load())
}

func (m *model) setPerm(p PermissionMode) {
	m.permMode.Store(int32(p))
}

// isPowerTool delegates to mow so the ask-gate vocabulary can never drift
// from the harness's own power-tool list.
func isPowerTool(name string) bool {
	return mow.IsPowerTool(name)
}

// permPreview formats tool args for the approval box: the actual command for
// bash, path + content head for write, old/new for edit — approving raw JSON
// blind was the old behavior.
func permPreview(name string, raw []byte) string {
	switch strings.ToLower(name) {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &a) == nil && strings.TrimSpace(a.Command) != "" {
			return "$ " + truncate(a.Command, 400)
		}
	case "write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(raw, &a) == nil && a.Path != "" {
			return a.Path + "\n@@ write @@\n" + strings.TrimRight(diffPreviewLines("+ ", a.Content, 14), "\n")
		}
	case "edit":
		var a struct {
			Path string `json:"path"`
			Old  string `json:"old_string"`
			New  string `json:"new_string"`
		}
		if json.Unmarshal(raw, &a) == nil && a.Path != "" {
			return a.Path + "\n@@ replace @@\n" +
				diffPreviewLines("- ", a.Old, 10) + strings.TrimRight(diffPreviewLines("+ ", a.New, 10), "\n")
		}
	}
	return truncate(string(raw), 180)
}

// diffPreviewLines prefixes each line (± for a diff) and caps the count, so the
// approval box shows a real before/after instead of a JSON blob.
func diffPreviewLines(prefix, s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	n := len(lines)
	if n > maxLines {
		lines = lines[:maxLines]
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(prefix + truncate(l, 120) + "\n")
	}
	if n > maxLines {
		b.WriteString(fmt.Sprintf("… (+%d more)\n", n-maxLines))
	}
	return b.String()
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

// syncInputChrome sets prompt prefix + colors.
// Busy: spinner + always-visible elapsed (e.g. "2.3s") so long TTFT still
// feels alive — the counter is the heartbeat even if a spinner frame stalls.
// Idle: ❯ or slash amber.
func (m *model) syncInputChrome() {
	st := m.ta.Styles()
	if m.busy {
		// Busy prompt is a short compose cue only — live tool/peer detail
		// lives on the activity band above the input rule (not here).
		var prompt string
		if reducedMotion() {
			prompt = m.theme.Accent.Render(glyphBrand)
		} else {
			prompt = m.theme.Muted.Render("…")
		}
		if len(m.queued) > 0 {
			prompt = m.theme.Muted.Render("…")
		}
		m.ta.Prompt = prompt + " "
		st.Focused.Prompt = lipgloss.NewStyle()
		st.Blurred.Prompt = lipgloss.NewStyle()
		// Dim draft text while a permission prompt owns the mode.
		if m.permWait != nil {
			st.Focused.Text = m.theme.Muted
		} else {
			st.Focused.Text = m.inputTextColor
		}
		st.Cursor.Color = m.inputPrompt.GetForeground()
		m.ta.SetStyles(st)
		return
	}
	m.ta.Prompt = m.cfg.PromptPrefix()
	switch {
	case m.editingPrompt:
		// Edit mode (arrow-up recall / /edit): accent prompt + tag so the
		// state is obvious; Esc cancels back to a blank prompt.
		m.ta.Prompt = m.theme.Accent.Render("edit ❯") + " "
		st.Focused.Text = m.inputTextColor
		st.Focused.Prompt = m.theme.Accent
		st.Cursor.Color = m.theme.Accent.GetForeground()
	case m.isSlashInput():
		st.Focused.Text = m.slashTextColor
		st.Focused.Prompt = m.slashPrompt
		st.Cursor.Color = m.slashPrompt.GetForeground()
	default:
		st.Focused.Text = m.inputTextColor
		st.Focused.Prompt = m.inputPrompt
		st.Cursor.Color = m.inputPrompt.GetForeground()
	}
	m.ta.SetStyles(st)
}

// busyHeartbeatInterval advances spinner frame + elapsed while a turn runs.
// Own chain — do not rely on bubbles spinner's tag-based Tick reschedule
// (a mismatched tag returns nil Cmd and the animation dies permanently).
const busyHeartbeatInterval = 100 * time.Millisecond

func (m *model) scheduleBusyHeartbeat() tea.Cmd {
	return tea.Tick(busyHeartbeatInterval, func(time.Time) tea.Msg {
		return busyHeartbeatMsg{}
	})
}

// advanceSpinnerFrame steps the spinner once. Uses tag=0 so bubbles never
// rejects the message; discards bubbles' own follow-up Tick cmd.
func (m *model) advanceSpinnerFrame() {
	if reducedMotion() {
		return
	}
	// tag 0 skips the "wrong tag → drop" check in bubbles/spinner.Update.
	m.spin, _ = m.spin.Update(spinner.TickMsg{
		Time: time.Now(),
		ID:   m.spin.ID(),
	})
}

// formatTokens is compact token display: 950, 12.3k, 1.2M.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatContextPct renders last-call input tokens vs gateway context_window.
// Returns warn=true at ≥80%. Avoids integer-floor "0% ctx" on large windows.
func formatContextPct(used, window int) (label string, warn bool) {
	label, level := formatContextPctLevel(used, window)
	return label, level >= 2
}

// formatContextPctLevel: 0 muted, 1 attention (≥50%), 2 warn (≥80%).
func formatContextPctLevel(used, window int) (label string, level int) {
	if used <= 0 || window <= 0 {
		return "", 0
	}
	ratio := float64(used) * 100 / float64(window)
	switch {
	case ratio >= 80:
		level = 2
	case ratio >= 50:
		level = 1
	default:
		level = 0
	}
	switch {
	case ratio < 0.1:
		return "<0.1% ctx", level
	case ratio < 1:
		return fmt.Sprintf("%.1f%% ctx", ratio), level
	default:
		return fmt.Sprintf("%.0f%% ctx", ratio), level
	}
}

// formatElapsed is a compact always-on busy timer:
//
//	0.0s … 9.9s  → tenths
//	10s … 59s    → whole seconds
//	1m+          → 1m 05s, 10m 00s, …
//	1h+          → 1h 2m, 1h 2m 03s, …
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	total := int(d.Round(time.Second) / time.Second)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	// Dense chrome: no spaces between units (6m05s, 1h02m03s).
	if total < 3600 {
		return fmt.Sprintf("%dm%02ds", total/60, total%60)
	}
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	switch {
	case m == 0 && s == 0:
		return fmt.Sprintf("%dh", h)
	case s == 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m == 0:
		return fmt.Sprintf("%dh%02ds", h, s)
	default:
		return fmt.Sprintf("%dh%dm%02ds", h, m, s)
	}
}

func (m *model) isSlashInput() bool {
	v := strings.TrimLeft(m.ta.Value(), " \t")
	return strings.HasPrefix(v, "/")
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

// kickEntryPretty glamours a finished assistant entry off the Update thread.
func (m *model) kickEntryPretty(idx int, text string, width int) tea.Cmd {
	if idx < 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	inner := max(16, width-roleGutterW)
	md := &m.md
	src := text
	return func() tea.Msg {
		body := renderMarkdownCached(md, src, inner, false)
		return entryPrettyMsg{idx: idx, width: width, src: src, body: body}
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

func (m *model) handlePermKey(k string) tea.Cmd {
	req := m.permWait
	if req == nil {
		return nil
	}
	// Cancel always works immediately (escape hatch).
	switch k {
	case "ctrl+c", "esc":
		m.clearPermWait()
		req.resp <- fmt.Errorf("cancelled")
		if m.cancel != nil {
			m.cancel()
		}
		m.add(kindStatus, "cancelled")
		m.layout()
		m.refreshVP()
		return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
	}
	// y/n/a require the strip to have painted and a short arm window so a
	// keystroke already in flight cannot approve an unread shell command.
	if k == "y" || k == "Y" || k == "n" || k == "N" || k == "a" || k == "A" {
		if !m.permDecisionArmed() {
			return nil
		}
	}
	switch k {
	case "y", "Y":
		m.clearPermWait()
		req.resp <- nil
		m.add(kindTool, "allowed · "+req.name)
	case "n", "N":
		m.clearPermWait()
		req.resp <- fmt.Errorf("denied by user")
		m.add(kindError, "denied · "+req.name)
	case "a", "A":
		m.clearPermWait()
		m.autoPower.Store(true)
		m.setPerm(PermAuto)
		req.resp <- nil
		m.add(kindStatus, "power tools always allowed this session")
	default:
		return nil
	}
	m.layout()
	m.refreshVP()
	return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
}

// permArmWindow is how long after the strip first paints before y/n/a count.
const permArmWindow = 280 * time.Millisecond

func (m *model) permDecisionArmed() bool {
	if m.permWait == nil || !m.permStripShown || m.permArmedAt.IsZero() {
		return false
	}
	return time.Since(m.permArmedAt) >= permArmWindow
}

func (m *model) clearPermWait() {
	m.permWait = nil
	m.permArmedAt = time.Time{}
	m.permStripShown = false
}

func (m *model) armPermWait(msg *permAskMsg) {
	m.permWait = msg
	m.permArmedAt = time.Now()
	m.permStripShown = false
}

// testArmPerm arms a permission as if the strip already painted (tests).
func (m *model) testArmPerm(name, args string, resp chan error) {
	m.armPermWait(&permAskMsg{name: name, args: args, resp: resp})
	m.permStripShown = true
	m.permArmedAt = time.Now().Add(-time.Second)
}

func (m *model) togglePerm() {
	if m.perm() == PermAuto {
		m.setPerm(PermAsk)
		m.autoPower.Store(false)
	} else {
		m.setPerm(PermAuto)
	}
	m.add(kindStatus, "perm "+glyphArrow+" "+m.perm().String())
	m.refreshVP()
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

// maybeCtxPressureStatus emits a one-shot status when session context is high.
func (m *model) maybeCtxPressureStatus() {
	limits := m.eng.Limits()
	if limits.ContextWindow <= 0 {
		return
	}
	ct := m.eng.ContextTokens()
	if ct <= 0 {
		// Cumulative usage re-counts prior context on every call and is not a
		// context-size estimate. With no latest-call input count, suppress the
		// warning rather than manufacture a misleading percentage.
		return
	}
	_, level := formatContextPctLevel(ct, limits.ContextWindow)
	if level < 2 || m.ctxPressureBand >= level {
		return
	}
	m.ctxPressureBand = level
	label, _ := formatContextPct(ct, limits.ContextWindow)
	m.add(kindStatus, label+" — consider a new session or shorter context")
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

func (m *model) View() tea.View {
	var content string
	switch {
	case m.quitting:
		content = ""
	case !m.ready:
		content = m.theme.Muted.Render(" mow ")
	case m.tooSmall():
		content = m.sizeWarnView()
	default:
		content = m.mainFrame()
		if m.effortPick != nil {
			content = placeOverlayCenter(m.effortPickerCard(), content, max(1, m.width), max(1, m.height))
		} else if m.modelPick != nil {
			content = placeOverlayCenter(m.modelPickerCard(), content, max(1, m.width), max(1, m.height))
		} else if m.showHelp {
			// Overlay help card on the live frame so transcript stays visible.
			content = placeOverlayCenter(m.helpCard(), content, max(1, m.width), max(1, m.height))
		}
	}
	v := tea.NewView(content)
	// BT v2: declare terminal features on the View (not NewProgram options).
	v.AltScreen = true
	// Mouse tracking steals the mouse from the terminal: drag-to-select text
	// is traded for wheel scroll. On by default so the wheel reaches the
	// transcript as a MouseWheelMsg instead of translated arrow keys, which
	// would recall or edit the prompt in the input box. MOW_MOUSE=0 opts out
	// and restores native selection; ctrl+u / ctrl+d still scroll either way.
	v.MouseMode = tea.MouseModeNone
	if m.mouseOn() {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
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

// mouseTrackingEnabled reports whether the app owns the mouse for wheel
// scroll. On by default: without tracking, terminals translate wheel events
// into arrow-key sequences, and a wheel-up would recall the last prompt into
// the input (or a wheel-down would move/clear it) instead of scrolling the
// transcript. Set MOW_MOUSE=0 (also false/off/no) to restore native terminal
// selection — keys ctrl+u / ctrl+d still scroll, and the arrow-burst guard
// keeps wheel noise out of the prompt.
func mouseTrackingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MOW_MOUSE"))) {
	case "0", "false", "off", "no", "none":
		return false
	}
	return true
}

// mouseOn reports whether mouse tracking is currently active. MOW_MOUSE sets
// the starting value; the select-mode toggle flips it at runtime so a user can
// grab the mouse back for copy/paste without restarting and losing the session.
func (m *model) mouseOn() bool {
	if m.mouseOff {
		return false
	}
	return mouseTrackingEnabled()
}

// tooSmall reports a terminal too cramped for usable chrome.
func (m *model) tooSmall() bool {
	return m.width > 0 && m.height > 0 &&
		(m.width < minTermWidth || m.height < minTermHeight)
}

func (m *model) sizeWarnView() string {
	msg := m.theme.Warn.Render(fmt.Sprintf(
		"terminal too small\n%d×%d  need ≥ %d×%d",
		m.width, m.height, minTermWidth, minTermHeight,
	))
	return lipgloss.Place(max(1, m.width), max(1, m.height), lipgloss.Center, lipgloss.Center, msg)
}

// mainFrame is header | transcript | [activity] | [permission] | input.
func (m *model) mainFrame() string {
	main := m.vp.View()
	if m.showWelcome {
		main = m.welcomeView()
	}
	// Scroll indicators are overlays inside the viewport's fixed height.
	// Appending a row here made mainFrame one line taller than layout() budgeted,
	// pushing the first transcript/user-prompt row above the terminal frame.
	if !m.showWelcome {
		indicator := ""
		if !m.followBottom && m.busy && (m.streamBuf != "" || m.toolCurrent != "") {
			indicator = m.theme.Muted.Render("↓ new output · end/pgdn to follow")
		} else if !m.followBottom && m.vp.TotalLineCount() > m.vp.VisibleLineCount() {
			pct := max(0, min(100, int(m.vp.ScrollPercent()*100)))
			indicator = m.theme.Muted.Faint(true).Render(fmt.Sprintf("↑ %d%%", pct))
		}
		if indicator != "" {
			alignRight := !m.busy || (m.streamBuf == "" && m.toolCurrent == "")
			main = overlayViewportFooter(main, indicator, m.width, m.vp.Height(), alignRight)
		}
	}
	parts := []string{m.renderHeader(), main}
	if band := m.renderActivityBand(); band != "" {
		parts = append(parts, band)
	}
	if act := m.renderPermissionStrip(); act != "" {
		parts = append(parts, act)
	}
	parts = append(parts, m.renderInput())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func overlayViewportFooter(main, indicator string, width, height int, alignRight bool) string {
	if height <= 0 || strings.TrimSpace(indicator) == "" {
		return main
	}
	lines := strings.Split(main, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	row := height - 1
	pad := max(0, (max(1, width)-lipgloss.Width(indicator))/2)
	if alignRight {
		pad = max(0, max(1, width)-lipgloss.Width(indicator))
	}
	lines[row] = strings.Repeat(" ", pad) + indicator
	return strings.Join(lines, "\n")
}

// compensateBandScroll keeps transcript content stable when the activity band
// toggles height (absent when idle).
func (m *model) compensateBandScroll(nowOn bool) {
	if !m.ready {
		return
	}
	if m.followBottom {
		// Pin after layout sets height.
		return
	}
	delta := activityBandRows
	if nowOn {
		// Band appears → viewport shrinks; keep top lines stable by
		// reducing YOffset when possible.
		y := m.vp.YOffset()
		if y >= delta {
			m.vp.SetYOffset(y - delta)
		}
		return
	}
	// Band disappears → viewport grows; push offset down so content doesn't jump.
	m.vp.SetYOffset(m.vp.YOffset() + delta)
}

func (m *model) welcomeView() string {
	th := m.theme
	var block string
	if strings.TrimSpace(m.cfg.WelcomeMessage) != "" {
		// Respect a configured splash verbatim (soft, no chrome).
		block = th.Muted.Render(m.cfg.WelcomeText())
	} else {
		// Branded but quiet: wordmark, one-line tagline, live context.
		brand := th.Title.Render(glyphWelcome + " mowi")
		tagline := th.Muted.Render("agentic coding in your terminal")
		ctx := th.Muted.Faint(true).Render(
			short(m.eng.Model(), 32) + "  " + glyphBullet + "  " + filepath.Base(m.eng.Workspace()),
		)
		block = lipgloss.JoinVertical(lipgloss.Center, brand, "", tagline, "", ctx)
	}
	// Single discoverability line from the *resolved* keymap (not hardcoded
	// esc/? — config overrides must stay accurate, same as helpCard).
	helpKey := m.cfg.Keys.Primary(m.cfg.Keys.Help)
	if helpKey == "" {
		helpKey = "?"
	}
	cancelKey := m.cfg.Keys.Primary(m.cfg.Keys.Cancel)
	if cancelKey == "" {
		cancelKey = "esc"
	}
	hint := th.Muted.Faint(true).Render(
		"type a message to start  " + glyphBullet + "  " + helpKey + " help  " + glyphBullet + "  " + cancelKey + " dismiss",
	)
	full := lipgloss.JoinVertical(lipgloss.Center, block, "", "", hint)
	h := m.vp.Height()
	if h < 1 {
		h = max(1, m.height-5)
	}
	return lipgloss.Place(max(1, m.width), h, lipgloss.Center, lipgloss.Center, full)
}

func (m *model) reportedUsageStatus() string {
	total := m.tokIn + m.tokOut + m.peerTokIn + m.peerTokOut
	if total <= 0 {
		return ""
	}
	lines := []string{
		"usage reported this run · " + formatTokens(total) + " total",
		"  host · " + formatTokens(m.tokIn) + " in · " + formatTokens(m.tokOut) + " out",
	}
	if peer := m.peerTokIn + m.peerTokOut; peer > 0 {
		lines = append(lines, "  peers · "+formatTokens(m.peerTokIn)+" in · "+formatTokens(m.peerTokOut)+" out")
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderHeader() string {
	// Quiet header, exactly 2 rows (clamped so soft-wrap cannot steal a row):
	//   left  = wordmark + workspace dir + active model/effort (identity, stable)
	//   right = priority-dropped chips (safety never truncated away)
	th := m.theme
	dot := th.Muted.Faint(true).Render(" " + glyphBullet + " ")
	left := th.Accent.Render("mowi")
	// Workspace comes first as quiet context; model is the strongest, rightmost
	// item in the identity group. Effort is adjacent but muted, so it reads as
	// model metadata without another slash/separator competing with the header.
	if ws := short(filepath.Base(m.eng.Workspace()), 20); ws != "" && ws != "." && ws != "/" {
		left += dot + th.Muted.Render(ws)
	}
	if mdl := short(strings.TrimSpace(m.eng.Model()), 32); mdl != "" {
		left += dot + th.Text.Render(mdl)
		if effort := short(strings.TrimSpace(m.eng.Effort()), 12); effort != "" {
			left += " " + th.Muted.Render("("+effort+")")
		}
	}

	type chip struct {
		text string
		must bool // never drop while present
	}
	var must, vanity []chip

	// Safety first (must): capability posture, always visible. Elevated powers
	// get warn chips (▲ write / ▲ shell = tools the model CAN use, not what it
	// is doing); the safe default shows a quiet "read only" so the posture is
	// never ambiguous — silence must never mean "fine".
	if m.eng.AllowWrite() {
		must = append(must, chip{th.Warn.Render(glyphWarn + " write"), true})
	}
	if m.eng.AllowShell() {
		must = append(must, chip{th.Warn.Render(glyphWarn + " shell"), true})
	}
	if !m.eng.AllowWrite() && !m.eng.AllowShell() {
		must = append(must, chip{th.Muted.Render("read only"), true})
	}
	if m.perm() == PermAsk {
		must = append(must, chip{th.Muted.Render("ask"), true})
	}

	// Context: must when warn; else vanity with higher keep priority than tokens.
	limits := m.eng.Limits()
	var ctxChip *chip
	if limits.ContextWindow > 0 {
		if ct := m.eng.ContextTokens(); ct > 0 {
			label, level := formatContextPctLevel(ct, limits.ContextWindow)
			var cs lipgloss.Style
			switch level {
			case 2:
				cs = th.Warn
			case 1:
				cs = th.Accent.Bold(false)
			default:
				cs = th.Muted
			}
			c := chip{cs.Render(label), level >= 2}
			ctxChip = &c
		}
	}
	if ctxChip != nil && ctxChip.must {
		must = append(must, *ctxChip)
	}

	// Vanity drop order (first dropped under width pressure):
	// focus → cost → tokens → goal → workspace; non-warn ctx kept longer.
	if m.focus == focusTranscript {
		vanity = append(vanity, chip{th.Accent.Render("focus:transcript"), false})
	}
	// Token activity observed this process (provider-reported; not a bill).
	// The header shows one transparent aggregate; /status carries provenance and
	// the host/peer input-output breakdown.
	if tok := m.tokIn + m.tokOut + m.peerTokIn + m.peerTokOut; tok > 0 {
		vanity = append(vanity, chip{th.Muted.Render(formatTokens(tok) + " tok"), false})
	}
	if gchip := goalHeaderChip(m.goalLive); gchip != "" {
		vanity = append(vanity, chip{th.Muted.Render(gchip), false})
	}
	if ctxChip != nil && !ctxChip.must {
		// Prefer keeping ctx over pure vanity: put at end (dropped last).
		vanity = append(vanity, chip{ctxChip.text, false})
	}

	ww := max(1, m.width)
	leftW := lipgloss.Width(left)
	budget := ww - leftW - 3 // leading space + min gap + trailing space
	if budget < 0 {
		budget = 0
	}

	join := func(cs []chip) string {
		if len(cs) == 0 {
			return ""
		}
		parts := make([]string, len(cs))
		for i, c := range cs {
			parts[i] = c.text
		}
		return strings.Join(parts, dot)
	}
	widthOf := func(cs []chip) int {
		if len(cs) == 0 {
			return 0
		}
		return lipgloss.Width(join(cs))
	}

	chosen := append([]chip{}, must...)
	keep := append([]chip{}, vanity...)
	for widthOf(append(chosen, keep...)) > budget && len(keep) > 0 {
		keep = keep[1:] // drop focus first, workspace/ctx last
	}
	right := join(append(chosen, keep...))
	gap := ww - leftW - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	if lipgloss.Width(line) > ww {
		right = join(must)
		gap = ww - leftW - lipgloss.Width(right) - 2
		if gap < 1 {
			gap = 1
		}
		line = " " + left + strings.Repeat(" ", gap) + right + " "
		if lipgloss.Width(line) > ww {
			line = xansi.Truncate(" "+left+" "+right+" ", ww, "")
		}
	}
	sep := m.theme.Sep.Render(strings.Repeat("─", ww))
	if lipgloss.Width(sep) > ww {
		sep = xansi.Truncate(sep, ww, "")
	}
	return line + "\n" + sep
}

// renderPermissionStrip is only for interactive tool permission prompts.
func (m *model) renderPermissionStrip() string {
	if m.permWait == nil {
		return ""
	}
	// First paint arms y/n/a decisions (see permDecisionArmed).
	m.permStripShown = true
	th := m.theme
	label := th.Warn.Render(glyphWarn + " permission")
	pending := len(m.permCh) + 1
	if pending > 1 {
		label += th.Muted.Render(fmt.Sprintf(" (%d of %d)", 1, pending))
	}
	if m.permWait.name != "" {
		label += th.Muted.Render("  " + m.permWait.name)
	}

	// Keep the decision keys pinned to the right edge. The command preview is
	// deliberately the part that yields, so y/n/a never wrap below it.
	ww := max(1, m.width)
	keyGap := "   "
	if ww < 56 {
		keyGap = " "
	}
	keys := th.Accent.Render("y") + th.Muted.Render(" allow"+keyGap) +
		th.Accent.Render("n") + th.Muted.Render(" deny"+keyGap) +
		th.Accent.Render("a") + th.Muted.Render(" always")
	keyW := lipgloss.Width(keys)
	// Status contributes one cell of padding at each edge.
	contentW := max(1, ww-2)
	left := label
	if m.permWait.args != "" {
		args := strings.ReplaceAll(m.permWait.args, "\n", " ⏎ ")
		budget := contentW - lipgloss.Width(label) - keyW - 3
		if budget > 0 {
			left += th.Muted.Faint(true).Render("  " + xansi.Truncate(args, budget, "…"))
		}
	}
	available := contentW - keyW - 1
	if lipgloss.Width(left) > available {
		left = xansi.Truncate(left, max(1, available), "…")
	}
	gap := max(1, contentW-lipgloss.Width(left)-keyW)
	line := left + strings.Repeat(" ", gap) + keys
	return th.Status.Render(xansi.Truncate(line, contentW, ""))
}

// renderActivityBand is the one-row live-work strip above the input rule.
// Ephemeral: never written into the transcript. Idle → empty (zero height).
func (m *model) renderActivityBand() string {
	if !m.activityBandVisible() {
		return ""
	}
	th := m.theme
	ww := max(1, m.width)
	var left, right []string

	if m.busy {
		// Left owns "what is happening" + a fast ticker next to the spinner;
		// right (below) owns the per-turn elapsed and status counts.
		spin := m.spinnerView()
		// Phase ticker hugs the spinner: seconds since the last activity
		// (tool start/end, stream chunk, thinking). It resets whenever the
		// model moves, so the eye tracks the current phase, not the whole turn.
		since := m.lastActivityAt
		if since.IsZero() {
			since = m.startedAt
		}
		if since.IsZero() {
			left = append(left, spin)
		} else {
			left = append(left, spin+" "+th.Muted.Render(formatElapsed(time.Since(since))))
		}

		label := ""
		switch {
		case m.permWait != nil:
			label = th.Warn.Render("waiting")
		case m.peerActive.Load() > 1:
			// Parallel peers: the count lives on the right; suppress the single
			// last-writer tool label so it is not mistaken for the whole set.
			label = ""
		case m.toolCurrent == "writing":
			// Phase marker after tools/peers — the host is composing its answer.
			label = th.Muted.Render("composing")
		case m.toolCurrent != "":
			// Budget: yield exactly what the right-aligned status needs, not a
			// fixed reserve — with a short right (e.g. just "12s") the tool
			// label keeps the full remaining width (layoutBand still clamps).
			rightW := 0
			if !m.startedAt.IsZero() {
				rightW += lipgloss.Width(th.Muted.Render(formatElapsed(time.Since(m.startedAt))))
			}
			if n := int(m.peerActive.Load()); n > 1 {
				rightW += 6 + lipgloss.Width(th.Muted.Render(fmt.Sprintf("%d peers", n)))
			}
			if n := len(m.queued); n > 0 {
				rightW += 6 + lipgloss.Width(th.Muted.Render(fmt.Sprintf("queued · %d", n)))
			}
			used := lipgloss.Width(spin) + 16 // spinner + phase ticker
			maxLab := max(8, ww-used-rightW-8)
			// Peer labels (acp_delegate) keep a wider floor — the delegated
			// task detail is the information, so a squeezed band must not
			// ellipsize it away the way a generic verb label tolerates.
			if strings.Contains(m.toolCurrent, ":") {
				maxLab = max(maxLab, minPeerLabelWidth)
			}
			lab := activityToolLabel(m.toolCurrent, m.toolCurrentArgs, maxLab)
			if strings.HasPrefix(lab, glyphArrow) || strings.Contains(m.toolCurrent, ":") {
				label = th.Muted.Render(lab)
			} else {
				label = th.Muted.Render(glyphTool + " " + lab)
			}
		case m.reasonBuf != "" && m.streamBuf == "":
			label = th.Muted.Render("thinking")
		case m.streamBuf != "":
			label = th.Muted.Render("composing")
		default:
			label = th.Muted.Render("working")
		}
		if label != "" {
			left = append(left, label)
		}
		// Stall note: silence since last stream/tool event (not turn start).
		if !m.lastActivityAt.IsZero() {
			silent := time.Since(m.lastActivityAt)
			if silent >= 45*time.Second && m.toolCurrent == "" && m.streamBuf == "" {
				left = append(left, th.Muted.Faint(true).Render("no output · "+formatElapsed(silent)))
			}
		}

		// Right: per-turn elapsed since submit — the clock that matters for
		// long turns (30s → 5m → 20m as one request grinds on). Pinned to the
		// right edge so it does not jitter as the left label changes across
		// the busy heartbeat.
		if !m.startedAt.IsZero() {
			right = append(right, th.Muted.Render(formatElapsed(time.Since(m.startedAt))))
		}

		// peerAgent is last-writer-wins; with parallel peers show the count so
		// one agent/tool is not mistaken for the whole set.
		if n := int(m.peerActive.Load()); n > 1 {
			right = append(right, th.Muted.Render(fmt.Sprintf("%d peers", n)))
		}
	}

	if n := len(m.queued); n > 0 {
		right = append(right, th.Muted.Render(fmt.Sprintf("queued · %d", n)))
	}
	if m.permWait != nil && !m.busy {
		left = append(left, th.Warn.Render("waiting · permission"))
	}

	sepDot := th.Muted.Faint(true).Render("  " + glyphBullet + "  ")
	line := layoutBand(ww, left, right, sepDot)
	// One blank row above the content so the band breathes under the
	// transcript the same way the input rule separates the draft below.
	// layoutChrome counts this as activityBandRows.
	return strings.Repeat(" ", ww) + "\n" + line
}

// layoutBand lays the left parts at the left edge and the right parts
// right-aligned to ww, separated by at least gap spaces. The right group is
// pinned to the right edge (padding is derived from the live left width) so it
// stays at a fixed column even as the left label grows/shrinks each frame —
// no jitter during the busy heartbeat. Truncation-safe at narrow widths: the
// left label yields first (keeping the elapsed/status visible), and the
// assembled line is hard-clamped to ww so terminals never soft-wrap.
func layoutBand(ww int, left, right []string, sep string) string {
	const gap = 2
	ls := " " + strings.Join(left, sep)
	rs := strings.Join(right, sep) + " " // trailing inset, matches header
	lw := lipgloss.Width(ls)
	rw := lipgloss.Width(rs)
	// Pathologically narrow: clamp the right group, then the whole line.
	if rw > ww-gap-1 {
		rs = xansi.Truncate(rs, max(1, ww-gap-1), "…")
		rw = lipgloss.Width(rs)
	}
	// Left yields to keep the right-aligned status on screen.
	if maxAvail := ww - rw - gap; lw > maxAvail {
		ls = xansi.Truncate(ls, max(1, maxAvail), "…")
		lw = lipgloss.Width(ls)
	}
	pad := ww - lw - rw
	if pad < gap {
		pad = gap
	}
	line := ls + strings.Repeat(" ", pad) + rs
	if lipgloss.Width(line) > ww {
		line = xansi.Truncate(line, ww, "")
	}
	return line
}

func (m *model) renderInput() string {
	m.syncInputChrome()
	// Same language as the header: a single horizontal rule, not a box.
	// Exactly 1 + ta.Height() rows; clamp width to avoid soft-wrap chrome break.
	ww := max(1, m.width)
	sep := m.theme.Sep.Render(strings.Repeat("─", ww))
	if lipgloss.Width(sep) > ww {
		sep = xansi.Truncate(sep, ww, "")
	}
	// Soft horizontal inset, matching header's leading/trailing space.
	// Textarea normally wraps at its configured width, but a stale width during
	// resize/SetValue can leave one visual row wider than the viewport. Clamp
	// every rendered row here as the final frame-safety boundary.
	body := m.theme.Input.MaxWidth(ww).Width(ww).Render(m.ta.View())
	body = clampFrameLines(body, ww)
	return sep + "\n" + body
}

func clampFrameLines(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = xansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
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

// helpCard is the help modal body (no placement). Used as an overlay on mainFrame.
func (m *model) helpCard() string {
	k := m.cfg.Keys
	th := m.theme
	cardW := min(max(24, m.width-4), 60)
	// Inner width available to a row inside the box (border 2 + pad 2).
	inner := max(12, cardW-4)

	// keyCol is the fixed gutter for the key/command token; pad the *plain*
	// token before styling so ANSI never throws off the alignment.
	const keyCol = 14
	row := func(keyStyle lipgloss.Style, token, desc string) string {
		tok := short(token, keyCol)
		pad := keyCol - lipgloss.Width(tok)
		if pad < 1 {
			pad = 1
		}
		line := "  " + keyStyle.Render(tok) + strings.Repeat(" ", pad) + th.Muted.Render(desc)
		return short(line, inner)
	}
	section := func(title string) string {
		return "  " + th.Muted.Faint(true).Render(strings.ToUpper(title))
	}
	rule := th.Sep.Render(strings.Repeat("╌", inner))

	scroll := k.Primary(k.ScrollUp) + "/" + k.Primary(k.ScrollDown)
	var b strings.Builder
	// Header: wordmark + subtitle, then a divider.
	b.WriteString(" " + th.Accent.Render("mowi") + th.Muted.Render("  keys & commands") + "\n")
	b.WriteString(" " + rule + "\n\n")

	b.WriteString(section("keys") + "\n")
	for _, r := range []struct{ key, desc string }{
		{k.Primary(k.Send), "send  (queues while busy)"},
		{k.Primary(k.Newline), "newline  (input grows)"},
		{scroll, "scroll transcript  (wheel / ctrl+u / ctrl+d)"},
		{k.Primary(k.Focus), "focus editor ↔ transcript"},
		{k.Primary(k.SelectMode), "select mode (release mouse to copy)"},
		{k.Primary(k.PeerExpand), "peer output: collapsed ↔ live text"},
		{k.Primary(k.PermCycle), "perm  auto ↔ ask"},
		{k.Primary(k.Clear), "clear transcript"},
		{k.Primary(k.Cancel), "cancel turn · dismiss"},
		{k.Primary(k.Help), "help  (? when input empty)"},
		{k.Primary(k.Quit), "quit"},
	} {
		b.WriteString(row(th.Accent, r.key, r.desc) + "\n")
	}

	b.WriteString("\n" + section("commands") + "\n")
	for _, c := range []struct{ cmd, desc string }{
		{"/model", "picker · /model <id> to jump"},
		{"/goal", "list · status · new · run"},
		{"/lsp", "recent post-edit diagnostics"},
		{"/review", "code review (same as mow review)"},
		{"/sec", "security review (same as mow sec)"},
		{"/btw", "aside — not added to context"},
		{"/steer", "guide the running turn (while busy)"},
		{"/search", "find in transcript (repeat to cycle)"},
		{"/retry", "regenerate the last answer"},
		{"/edit", "edit last message  (or ↑ when empty)"},
		{"/sessions", "resume a session"},
		{"/copy", "yank last answer"},
		{"/status", "session details"},
		{"/perm", "auto | ask"},
	} {
		b.WriteString(row(th.SlashCmd, c.cmd, c.desc) + "\n")
	}
	// Trivial commands share one quiet row (keeps /help /clear /quit discoverable).
	b.WriteString("  " + th.SlashCmd.Render("/help") + th.Muted.Render("   ") +
		th.SlashCmd.Render("/clear") + th.Muted.Render("   ") +
		th.SlashCmd.Render("/quit") + "\n")

	b.WriteString("\n" + section("permission") + "\n")
	b.WriteString(row(th.Accent, "y / n / a", "allow · deny · always") + "\n")
	b.WriteString(row(th.Muted, "header", "read only = safe · ▲ write / ▲ shell = those tools on") + "\n")

	b.WriteString("\n " + th.Muted.Faint(true).Render(short(k.Primary(k.Cancel)+" or ? to close", inner)))

	return th.Box.Width(cardW).Render(strings.TrimRight(b.String(), "\n"))
}

// inputHeightCap is the max textarea rows given terminal chrome.
// header(2) + input top rule(1) + min transcript(5) [+ perm strip].
func (m *model) inputHeightCap() int {
	capH := inputMaxHeight
	if m.height > 0 {
		room := m.height - 2 - 1 - 5
		if m.permWait != nil {
			room--
		}
		if room < inputMinHeight {
			room = inputMinHeight
		}
		if room < capH {
			capH = room
		}
	}
	return capH
}

// applyInputHeightCap sets DynamicHeight bounds from the terminal size.
func (m *model) applyInputHeightCap() {
	m.ta.DynamicHeight = true
	m.ta.MinHeight = inputMinHeight
	m.ta.MaxHeight = m.inputHeightCap()
}

// clampInputHeight enforces MaxHeight after a resize or cap change.
// Returns true when height was reduced (caller should re-layout).
func (m *model) clampInputHeight() bool {
	capH := m.inputHeightCap()
	m.ta.MaxHeight = capH
	if m.ta.Height() > capH {
		m.ta.SetHeight(capH)
		return true
	}
	return false
}

// syncInputHeight keeps DynamicHeight bounds in sync and re-applies height from
// content when needed (e.g. after SetValue outside Update). Returns true if
// Height() changed so the caller can re-layout chrome.
func (m *model) syncInputHeight() bool {
	before := m.ta.Height()
	m.applyInputHeightCap()
	// SetValue/Insert path: force a content-based height by re-setting value
	// only when DynamicHeight did not run (e.g. tests). Prefer re-measure via
	// a no-op InsertString of empty when value already set — SetValue works.
	v := m.ta.Value()
	// Re-trigger recalculateHeight inside bubbles (private) through SetValue.
	// Cursor moves to end — OK for tests and rare non-Update callers.
	m.ta.SetValue(v)
	m.clampInputHeight()
	return m.ta.Height() != before
}

// layoutChrome returns fixed chrome row counts: header, activity band, perm strip, input, total.
// Must match renderHeader / renderActivityBand / renderPermissionStrip / renderInput.
// activityBandRows is the chrome height of the activity band when visible:
// 1 blank top pad row + 1 content row.
const activityBandRows = 2

func (m *model) layoutChrome() (header, band, perm, input, chrome int) {
	header = 2 // title + rule
	if m.activityBandVisible() {
		band = activityBandRows
	}
	if m.permWait != nil {
		perm = 1
	}
	input = m.ta.Height() + 1 // rule + textarea
	chrome = header + band + perm + input
	return
}

func (m *model) activityBandVisible() bool {
	if m.permWait != nil {
		return true
	}
	if len(m.queued) > 0 {
		return true
	}
	if !m.busy {
		return false
	}
	// Busy: show band for spinner/elapsed/tool/thinking.
	return true
}

func (m *model) layout() {
	// Soft-wrapped transcript lines would desync chrome math (see roleBlock clamp).
	m.applyInputHeightCap()
	m.clampInputHeight()
	wantBand := m.activityBandVisible()
	if m.ready && wantBand != m.activityBandOn {
		m.compensateBandScroll(wantBand)
		m.activityBandOn = wantBand
	}
	_, _, _, _, chrome := m.layoutChrome()
	vh := m.height - chrome
	if vh < 3 && m.ta.Height() > inputMinHeight {
		m.ta.SetHeight(inputMinHeight)
		_, _, _, _, chrome = m.layoutChrome()
		vh = m.height - chrome
	}
	// Never force viewport taller than remaining space — tooSmall() covers
	// unusable terminals; keep vh ≥ 1 so the widget stays valid.
	if vh < 1 {
		vh = 1
	}
	w := max(1, m.width)
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(vh))
		m.vp.MouseWheelEnabled = true
		// Scroll keys from config; letter bindings stripped so typing never scrolls.
		up := m.cfg.Keys.All(m.cfg.Keys.ScrollUp)
		down := m.cfg.Keys.All(m.cfg.Keys.ScrollDown)
		if len(up) == 0 {
			up = []string{"ctrl+u"}
		}
		if len(down) == 0 {
			down = []string{"ctrl+d"}
		}
		m.vp.KeyMap = viewport.KeyMap{
			HalfPageUp:   key.NewBinding(key.WithKeys(up...), key.WithHelp(m.cfg.Keys.Primary(m.cfg.Keys.ScrollUp), "scroll up")),
			HalfPageDown: key.NewBinding(key.WithKeys(down...), key.WithHelp(m.cfg.Keys.Primary(m.cfg.Keys.ScrollDown), "scroll down")),
		}
		m.ready = true
	} else {
		m.vp.SetWidth(w)
		m.vp.SetHeight(vh)
	}
	// Width includes the textarea's horizontal padding, while textarea.SetWidth
	// receives the content frame width. Keep both in agreement so the first
	// prompt row does not exceed the visible terminal viewport.
	inputFrameW := max(1, m.width-2)
	m.ta.SetWidth(inputFrameW)
}

func (m *model) add(kind entryKind, text string) {
	m.addAt(kind, text, time.Now())
}

// addAt is add with an explicit timestamp (zero = no stamp line; used for
// resumed history whose original times are unknown).
func (m *model) addAt(kind entryKind, text string, at time.Time) {
	// One choke point for terminal safety: every transcript entry — model
	// output, tool results, diffs, perm args — is stripped of control
	// sequences before it is stored or painted.
	m.entries = append(m.entries, entry{kind: kind, text: sanitizeDisplay(text), at: at})
	m.gcOldEntryText()
	m.invalidateHistoryCache()
}

// bumpToolTally counts a finished call into this turn's single tool line,
// replacing its content instead of appending a line per call. A lone call
// keeps the richer "name · 0.4s" form.
// bumpToolTally counts a finished call into this turn's single tool line,
// updating the transcript entry in place.
func (m *model) bumpToolTally(name, line string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	name = strings.ToLower(strings.TrimSpace(name))
	found := false
	for i := range m.toolTally {
		if m.toolTally[i].name == name {
			m.toolTally[i].count++
			found = true
			break
		}
	}
	if !found {
		m.toolTally = append(m.toolTally, toolCount{name: name, count: 1})
	}
	m.renderToolTallyLine(line)
}

// bumpToolError folds a failed tool into this turn's tool tally line instead of
// stacking a red kindError row per failure (line_hash misses were flooding the UI).
func (m *model) bumpToolError(line string) {
	m.toolErrCount++
	// Prefer a short suffix: "edit · error · line_hash…" → "line_hash…"
	short := strings.TrimSpace(line)
	if i := strings.LastIndex(short, " · "); i >= 0 {
		short = strings.TrimSpace(short[i+3:])
	}
	short = strings.TrimPrefix(short, "error · ")
	short = strings.TrimPrefix(short, "error: ")
	if short == "" {
		short = "error"
	}
	m.toolErrLast = short
	// Count the tool name when present so edit failures still show under edit.
	name := "tool"
	if fields := strings.Fields(line); len(fields) > 0 {
		name = strings.ToLower(fields[0])
	}
	// Also increment tally for the tool so "edit ×3" reflects attempts.
	found := false
	for i := range m.toolTally {
		if m.toolTally[i].name == name {
			m.toolTally[i].count++
			found = true
			break
		}
	}
	if !found && name != "tool" {
		m.toolTally = append(m.toolTally, toolCount{name: name, count: 1})
	}
	m.renderToolTallyLine(line)
}

// renderToolTallyLine writes/updates the single kindTool progress line for this turn.
func (m *model) renderToolTallyLine(singleLine string) {
	total := 0
	parts := make([]string, 0, len(m.toolTally)+1)
	for _, t := range m.toolTally {
		total += t.count
		if t.count == 1 {
			parts = append(parts, t.name)
		} else {
			parts = append(parts, fmt.Sprintf("%s ×%d", t.name, t.count))
		}
	}
	text := strings.Join(parts, " · ")
	if total == 1 && m.toolErrCount == 0 && strings.TrimSpace(singleLine) != "" {
		text = singleLine
	}
	if m.toolErrCount > 0 && m.toolErrLast != "" {
		errBit := m.toolErrLast
		if m.toolErrCount > 1 {
			errBit = fmt.Sprintf("%s ×%d", m.toolErrLast, m.toolErrCount)
		}
		if text == "" {
			text = "⚠ " + errBit
		} else {
			text = text + " · ⚠ " + errBit
		}
	}
	if text == "" {
		return
	}
	if m.toolLineIdx >= 0 && m.toolLineIdx < len(m.entries) && m.entries[m.toolLineIdx].kind == kindTool {
		e := &m.entries[m.toolLineIdx]
		e.text = sanitizeDisplay(text)
		e.view, e.viewW = "", 0
		m.invalidateHistoryCache()
		return
	}
	m.add(kindTool, text)
	m.toolLineIdx = len(m.entries) - 1
}

// addLSPProblems adds a compact post-edit diagnostics transcript group and
// retains only the newest batch for each path. The engine bounds diagnostics,
// but clamp again at the UI boundary because events are host-facing input.
func (m *model) addLSPProblems(problems lspProblemsEvent) {
	if problems.count <= 0 {
		return
	}
	if len(problems.diagnostics) > mow.MaxLSPDiagnostics {
		problems.diagnostics = problems.diagnostics[:mow.MaxLSPDiagnostics]
	}
	sort.SliceStable(problems.diagnostics, func(i, j int) bool {
		return lspSeverityRank(string(problems.diagnostics[i].Severity)) > lspSeverityRank(string(problems.diagnostics[j].Severity))
	})
	for i := range m.lspProblems {
		if m.lspProblems[i].path == problems.path {
			m.lspProblems = append(m.lspProblems[:i], m.lspProblems[i+1:]...)
			break
		}
	}
	m.lspProblems = append([]lspProblemsEvent{problems}, m.lspProblems...)
	if len(m.lspProblems) > maxLSPProblemPaths {
		m.lspProblems = m.lspProblems[:maxLSPProblemPaths]
	}
	m.addLSPProblemLines(problems, maxLSPProblemEntries, false)
}

func lspSeverityRank(severity string) int {
	switch severity {
	case "error":
		return 4
	case "warning":
		return 3
	case "information":
		return 2
	default:
		return 1
	}
}

// lspDiagnosticText deliberately gets Source through reflection so mowi remains
// compatible with both sides of the frozen contract while older mow modules are
// in use. Missing Source is simply omitted.
func lspDiagnosticText(path string, d mow.Diagnostic) string {
	text := fmt.Sprintf("lsp · %s:%d %s", path, d.Line, d.Message)
	v := reflect.ValueOf(d)
	if source := v.FieldByName("Source"); source.IsValid() && source.Kind() == reflect.String && source.String() != "" {
		text += " · " + source.String()
	}
	return text
}

func (m *model) addLSPDiagnostic(path string, d mow.Diagnostic) {
	text := lspDiagnosticText(path, d)
	switch d.Severity {
	case "error":
		m.add(kindError, text)
	case "warning":
		m.add(kindWarn, text)
	default:
		m.add(kindStatus, text)
	}
}

func (m *model) addLSPProblemLines(problems lspProblemsEvent, limit int, header bool) int {
	if header {
		m.add(kindStatus, fmt.Sprintf("lsp · %s · %d problem(s)", problems.path, problems.count))
	}
	shown := min(limit, len(problems.diagnostics))
	for _, d := range problems.diagnostics[:shown] {
		m.addLSPDiagnostic(problems.path, d)
	}
	if more := problems.count - shown; more > 0 {
		hiddenErrors := 0
		for _, d := range problems.diagnostics[shown:] {
			if d.Severity == "error" {
				hiddenErrors++
			}
		}
		footer := fmt.Sprintf("lsp · %s · …%d more", problems.path, more)
		if hiddenErrors > 0 {
			footer += fmt.Sprintf(" (%d errors)", hiddenErrors)
		}
		m.add(kindStatus, footer)
	}
	return shown
}

// showLSPProblems lists newest retained batches first without replaying an
// unbounded session transcript.
func (m *model) showLSPProblems() {
	if len(m.lspProblems) == 0 {
		m.add(kindStatus, "lsp · none")
		return
	}
	lines := 0
	batches := 0
	for _, problems := range m.lspProblems {
		if batches >= maxLSPRecentBatches || lines >= maxLSPRecentLines {
			break
		}
		m.add(kindStatus, fmt.Sprintf("lsp · %s · %d problem(s)", problems.path, problems.count))
		lines++
		for _, d := range problems.diagnostics {
			if lines >= maxLSPRecentLines {
				break
			}
			m.addLSPDiagnostic(problems.path, d)
			lines++
		}
		batches++
	}
	if batches < len(m.lspProblems) || lines >= maxLSPRecentLines {
		m.add(kindStatus, "lsp · …older omitted")
	}
}

// resetToolTally starts a fresh tally for a new turn.
func (m *model) resetToolTally() {
	m.toolTally = nil
	m.toolLineIdx = -1
	m.toolCurrent = ""
	m.toolErrCount = 0
	m.toolErrLast = ""
}

// clearTranscript wipes entries plus every index keyed by entry position —
// stale prettyWant/lineStart indices otherwise force-pretty unrelated future
// entries.
func (m *model) clearTranscript() {
	m.entries = nil
	m.searchTerm = ""
	m.searchHits = nil
	m.searchIdx = 0
	m.toolLineIdx = -1
	m.entryHeights = nil
	m.entryLineStart = nil
	m.prettyWant = nil
	m.showWelcome = m.cfg.ShowWelcome()
	m.refreshVP()
}

func (m *model) lines() []string {
	out := make([]string, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.text
	}
	return out
}

func (m *model) invalidateHistoryCache() {
	m.historyCache = ""
	m.historyCacheW = 0
	m.historyCacheN = -1
}

// ensureHistoryCache rebuilds finished transcript (virtualized pretty window).
func (m *model) ensureHistoryCache(w int) {
	m.ensureHistoryCacheVirtual(w)
}

// applyVPContent sets viewport from historyCache + optional live streamFrame.
func (m *model) applyVPContent() {
	w := max(24, m.vp.Width()-2)
	m.ensureHistoryCache(w)
	content := m.historyCache
	if m.busy && m.streamFrame != "" && m.streamFrameW == w {
		if content != "" {
			// Match the committed inter-entry separator ("\n\n", one blank line)
			// so the live answer sits exactly where it lands after commit — no
			// downward shift when streaming finishes and the entry is stored.
			content += "\n\n"
		}
		content += m.streamFrame
	}
	// Preserve scroll when user has paged up. SetContent can clamp/jump; re-apply.
	y := m.vp.YOffset()
	wasFollowing := m.followBottom
	m.vp.SetContent(content)
	if wasFollowing {
		m.vp.GotoBottom()
		m.followBottom = true
	} else {
		m.vp.SetYOffset(y)
		// Defensive: never re-enable follow just because content shrunk.
		m.followBottom = false
	}
}

// refreshVP rebuilds history cache and applies viewport (full refresh).
func (m *model) refreshVP() {
	m.invalidateHistoryCache()
	m.applyVPContent()
}

// renderEntry renders a single transcript item.
func (m *model) renderEntry(e entry, width int) string {
	inner := max(16, width-roleGutterW)
	gcNote := ""
	if e.gc {
		gcNote = m.metaLine(m.theme.Muted.Faint(true).Render(glyphBullet+" turn trimmed (memory)"), width) + "\n"
	}
	switch e.kind {
	case kindUser:
		// Soft left bar + fill — prompt block in a document, not a chat bubble.
		// -2 for horizontal pad inside RoleUserBg; the inline timestamp shares
		// the first row, so reserve its cells too or that row overflows the
		// viewport and userBlock truncates the text away.
		body := m.renderUser(e.text, max(12, inner-2-userStampWidth(e.at)))
		return gcNote + m.renderTurn(true, body, e.at, width)
	case kindAssistant:
		// Accent gutter + content. Never glamour here — pretty is async.
		body := wordWrap(e.text, inner)
		return gcNote + m.renderTurn(false, body, e.at, width)
	case kindTool:
		return m.metaLine(m.theme.Muted.Render(glyphTool+" "+e.text), width)
	case kindDiff:
		return gcNote + m.renderDiffEntry(e.text, width) + "\n"
	case kindError:
		// Full error color + glyph; never dimmed (error > tool > status).
		return m.metaLine(m.theme.Error.Bold(true).Render(glyphError+" "+e.text), width) + "\n"
	case kindPerm:
		// Color the body as a diff: path/@@ headers as meta, +/- lines as
		// add/del. Non-diff previews (bash "$ …") are left untouched.
		body := colorDiffLines(m.theme, e.text)
		return m.theme.Box.Width(min(width, 72)).Render(
			m.theme.Warn.Render(glyphWarn+" permission")+"\n"+body,
		) + "\n"
	case kindStatus:
		// Quieter than tools/errors by muted color + bullet (not Faint — C4
		// contrast: status still carries meaning on dim terminals).
		return m.metaLine(m.theme.Muted.Render(glyphBullet+" "+e.text), width)
	default:
		return m.theme.Muted.Render(e.text)
	}
}

// userStampWidth is the cell cost of the inline timestamp on a user block's
// first row (" 15:04  "), or 0 when the entry carries no time.
func userStampWidth(at time.Time) int {
	if at.IsZero() {
		return 0
	}
	return lipgloss.Width(" "+formatTurnTime(at, time.Now())) + 2
}

func (m *model) renderTurn(user bool, body string, at time.Time, width int) string {
	if user {
		return m.userBlock(body, at) + "\n"
	}
	return m.roleBlock(false, body) + "\n"
}

// userBlock paints a user prompt as a soft background block with the muted
// timestamp inline on the first line.
func (m *model) userBlock(body string, at time.Time) string {
	maxW := max(1, m.width)
	prefix := m.rolePrefix(true)
	stamp := ""
	if !at.IsZero() {
		stamp = formatTurnTime(at, time.Now())
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		var row string
		if i == 0 && stamp != "" {
			row = prefix + m.theme.StampUser.Render(" "+stamp+"  ") + m.theme.RoleUserBg.Render(line+" ")
		} else {
			row = prefix + m.theme.RoleUserBg.Render(" "+line+" ")
		}
		if lipgloss.Width(row) > maxW {
			row = xansi.Truncate(row, maxW, "")
		}
		lines[i] = row
	}
	return strings.Join(lines, "\n")
}

// formatTurnTime is compact wall-clock for transcript turns.
// Same calendar day → 15:04; same year → Jan 2 15:04; else 2006-01-02 15:04.
func formatTurnTime(at, now time.Time) string {
	at = at.In(time.Local)
	now = now.In(time.Local)
	ay, am, ad := at.Date()
	ny, nm, nd := now.Date()
	if ay == ny && am == nm && ad == nd {
		return at.Format("15:04")
	}
	if ay == ny {
		return at.Format("Jan 2 15:04")
	}
	return at.Format("2006-01-02 15:04")
}

// metaLine indents tool/status/error under the content column (past the role gutter).
func (m *model) metaLine(text string, width int) string {
	pad := strings.Repeat(" ", roleGutterW)
	row := pad + text
	if width > 0 && lipgloss.Width(row) > width {
		row = xansi.Truncate(row, width, "")
	}
	return row
}

// renderDiffEntry paints a file-change tool result as a compact review card:
// verb + basename (+ muted path) + +N/−M stats, then tinted dual-number hunks.
func (m *model) renderDiffEntry(text string, width int) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	head := lines[0]
	// "created path", "wrote path", "edited path"
	op, path, _ := strings.Cut(head, " ")
	path = strings.TrimSpace(path)
	base := path
	if path != "" {
		base = filepath.Base(path)
	}
	body := ""
	if len(lines) > 1 {
		body = strings.Join(lines[1:], "\n")
	}
	add, del := countDiffStats(body)

	th := m.theme
	var verb string
	switch op {
	case "created":
		verb = th.DiffAdd.UnsetBackground().Render("created")
	case "wrote":
		verb = th.DiffMeta.Render("wrote")
	case "edited":
		verb = th.DiffMeta.Render("edited")
	default:
		verb = th.DiffMeta.Render(op)
	}
	name := th.Accent.Render(base)
	// Show parent path when not just a basename (context without full noise).
	var pathHint string
	if path != "" && path != base {
		dir := filepath.Dir(path)
		if dir != "." && dir != "/" {
			pathHint = th.Muted.Faint(true).Render("  " + dir)
		}
	}
	stats := formatDiffStats(th, add, del, op)

	gutter := strings.Repeat(" ", roleGutterW)
	title := gutter + verb + "  " + name + pathHint
	if stats != "" {
		title += "  " + stats
	}
	if width > 0 && lipgloss.Width(title) > width {
		title = xansi.Truncate(title, width, "")
	}
	if body == "" {
		return title
	}
	// Collapse very large diffs to a summary + first N lines (P2 polish).
	const diffBodyMaxLines = 40
	bodyLines := strings.Split(body, "\n")
	if len(bodyLines) > diffBodyMaxLines {
		kept := strings.Join(bodyLines[:diffBodyMaxLines], "\n")
		more := len(bodyLines) - diffBodyMaxLines
		rest := strings.Join(bodyLines[diffBodyMaxLines:], "\n")
		ra, rd := countDiffStats(rest)
		fold := fmt.Sprintf("… %d more lines", more)
		if ra > 0 || rd > 0 {
			fold = fmt.Sprintf("… %d more lines (+%d −%d)", more, ra, rd)
		}
		body = kept + "\n" + fold
	}
	inner := width
	if inner > 0 {
		inner = max(24, width-roleGutterW)
	}
	colored := renderPrettyDiff(th, body, inner)
	// Keep dual-number columns aligned under the title (no extra indent).
	indented := indentLines(colored, gutter)
	return title + "\n" + indented
}

// formatDiffStats is "+3 −1" for the card header (empty when nothing counted).
func formatDiffStats(th theme, add, del int, op string) string {
	if add == 0 && del == 0 {
		return ""
	}
	var parts []string
	if add > 0 {
		parts = append(parts, th.DiffAdd.UnsetBackground().Render(fmt.Sprintf("+%d", add)))
	} else if op == "created" {
		// create with empty body still says new file via hunk label
	}
	if del > 0 {
		parts = append(parts, th.DiffDel.UnsetBackground().Render(fmt.Sprintf("−%d", del)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func indentLines(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// rolePrefix is the plain left gutter. The colored bar is gone — the user
// block's background fill and the indented content column carry the role
// distinction with less ink.
func (m *model) rolePrefix(_ bool) string {
	return strings.Repeat(" ", roleGutterW)
}

// roleBlock paints a left-aligned role column (agent transcript, not chat UI).
// User prompts get a soft fill; assistant content does not (glamour ANSI-safe).
// Lines are clamped to terminal width so the shell never soft-wraps a row.
func (m *model) roleBlock(user bool, body string) string {
	maxW := max(1, m.width)
	prefix := m.rolePrefix(user)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if user {
			// Soft pad inside fill — bar stays a pure color stripe.
			line = m.theme.RoleUserBg.Render(" " + line + " ")
		}
		row := prefix + line
		if lipgloss.Width(row) > maxW {
			row = xansi.Truncate(row, maxW, "")
		}
		lines[i] = row
	}
	return strings.Join(lines, "\n")
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
