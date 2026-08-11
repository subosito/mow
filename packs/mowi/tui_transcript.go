package mowi

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/subosito/mow"
)

// toolCount is one name's call count in the per-turn tool tally.
type toolCount struct {
	name  string
	count int
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
// Large bodies collapse; the full text is available via the diff overlay
// (ViewDiff key, default ctrl+e).
func (m *model) renderDiffEntry(text string, width int) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	op, path, body := parseDiffEntry(text)
	base := path
	if path != "" {
		base = filepath.Base(path)
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
	// Discoverability: the expand key is quiet on the title so compact cards
	// stay scannable; collapsed bodies also carry an expand hint (below).
	if width > 0 && lipgloss.Width(title) > width {
		title = xansi.Truncate(title, width, "")
	}
	if body == "" {
		return title
	}
	// Collapse very large diffs to a summary + first N lines (P2 polish).
	// The overlay paints the uncollapsed body so nothing is lost.
	const diffBodyMaxLines = 40
	collapsed := false
	bodyLines := strings.Split(body, "\n")
	if len(bodyLines) > diffBodyMaxLines {
		body = collapseDiffBody(body, diffBodyMaxLines)
		collapsed = true
	}
	inner := width
	if inner > 0 {
		inner = max(24, width-roleGutterW)
	}
	colored := renderPrettyDiffPath(th, body, path, inner)
	// Keep dual-number columns aligned under the title (no extra indent).
	indented := indentLines(colored, gutter)
	out := title + "\n" + indented
	if collapsed {
		key := m.cfg.Keys.Primary(m.cfg.Keys.ViewDiff)
		if key == "" {
			key = "ctrl+e"
		}
		hint := gutter + th.Muted.Faint(true).Render(key+" expand full diff")
		if width > 0 && lipgloss.Width(hint) > width {
			hint = xansi.Truncate(hint, width, "")
		}
		out += "\n" + hint
	}
	return out
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
