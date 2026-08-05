package mowi

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

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

func (m *model) isSlashInput() bool {
	v := strings.TrimLeft(m.ta.Value(), " \t")
	return strings.HasPrefix(v, "/")
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
	case "/ext":
		return m.handleExtSlash(parts)
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

func (m *model) handleExtSlash(parts []string) tea.Cmd {
	if len(parts) == 1 || parts[1] == "list" || parts[1] == "status" {
		list := ext.ListExtensions(0)
		if len(list) == 0 {
			m.add(kindStatus, "ext · no extensions registered")
			m.refreshVP()
			return nil
		}
		var b strings.Builder
		b.WriteString("extensions:\n")
		for _, info := range list {
			fmt.Fprintf(&b, "  • %-20s  %-8s  %s\n", info.Name, info.Kind, info.Status)
		}
		m.add(kindStatus, strings.TrimRight(b.String(), "\n"))
		m.refreshVP()
		return nil
	}
	action := strings.ToLower(parts[1])
	switch action {
	case "on", "enable":
		if len(parts) < 3 {
			m.add(kindError, "usage: /ext on <name>")
			m.refreshVP()
			return nil
		}
		target := parts[2]
		if ext.SetExtensionEnabled(target, true) {
			m.add(kindStatus, "ext "+glyphArrow+" enabled "+target)
		} else {
			m.add(kindError, "ext · no extension matching "+target)
		}
	case "off", "disable":
		if len(parts) < 3 {
			m.add(kindError, "usage: /ext off <name>")
			m.refreshVP()
			return nil
		}
		target := parts[2]
		if ext.SetExtensionEnabled(target, false) {
			m.add(kindStatus, "ext "+glyphArrow+" disabled "+target)
		} else {
			m.add(kindError, "ext · no extension matching "+target)
		}
	default:
		m.add(kindError, "usage: /ext [list|on|off <name>]")
	}
	m.refreshVP()
	return nil
}
