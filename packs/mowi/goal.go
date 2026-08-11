package mowi

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/subosito/mow/packs/goal"
)

// goalEventMsg is a progress event from ext/goal (Subscribe → channel → poll).
type goalEventMsg struct {
	ev goal.Event
}

// goalDoneMsg ends a TUI-driven goal.Run / RunSpec.
type goalDoneMsg struct {
	state goal.State
	err   error
}

func (m *model) initGoalBus() {
	if m.goalCh != nil {
		return
	}
	m.goalCh = make(chan goal.Event, 32)
	m.goalUnsub = goal.Subscribe(func(e goal.Event) {
		select {
		case m.goalCh <- e:
		default:
			// drop if UI is behind; state is still on disk
		}
	})
}

func (m *model) pollGoal() tea.Cmd {
	if m.goalCh == nil {
		return nil
	}
	return func() tea.Msg {
		e, ok := <-m.goalCh
		if !ok {
			return nil
		}
		return goalEventMsg{ev: e}
	}
}

func (m *model) handleGoalEvent(ev goal.Event) tea.Cmd {
	st := ev.State
	// Live chip for header while running.
	if st.Status == goal.StatusRunning || ev.Kind == goal.EventStart || ev.Kind == goal.EventStep {
		cp := st
		m.goalLive = &cp
	}
	// Tick the session token counter from the goal's running total (delta
	// since the last event so it stays session-cumulative, live per step).
	// The baseline is per-goal: switching goals resets it (a fresh goal's
	// cumulative starts near zero), while rerunning the SAME goal keeps it so
	// only new tokens count (State.InputTokens is cumulative across runs).
	if m.goalTokID != st.ID {
		m.goalTokID = st.ID
		m.goalTokIn, m.goalTokOut = 0, 0
	}
	if di := st.InputTokens - m.goalTokIn; di > 0 {
		m.tokIn += di
		m.goalTokIn = st.InputTokens
	}
	if do := st.OutputTokens - m.goalTokOut; do > 0 {
		m.tokOut += do
		m.goalTokOut = st.OutputTokens
	}
	// Partial and blocked states are terminal progress signals even when an
	// older runner labels the event differently. A blocked goal needs an
	// unmistakable prompt for the human decision that resumes it.
	if ev.Kind == goal.EventBlocked || st.Status == goal.StatusBlocked {
		question := strings.TrimSpace(st.Question)
		if question == "" {
			question = strings.TrimSpace(ev.Text)
		}
		line := fmt.Sprintf("goal · %s BLOCKED", st.ID)
		if question != "" {
			line += " — " + question
		}
		m.add(kindError, line)
		// A merge-conflict escalation keeps the worktree + mow-wt- branch on
		// disk for human inspection; surface the explicit cleanup affordance.
		if goalWorktreeConflict(question) {
			m.add(kindStatus, fmt.Sprintf("goal · %s blocked — leftover worktree: /goal worktrees prune", st.ID))
		}
		m.goalLive = nil
		m.refreshVP()
		return m.pollGoal()
	}
	if ev.Kind == goal.EventPartial || st.Status == goal.StatusPartial {
		partial := strings.TrimSpace(st.Partial)
		if partial == "" {
			partial = strings.TrimSpace(ev.Text)
		}
		line := fmt.Sprintf("goal · %s partial", st.ID)
		if partial != "" {
			line += " · " + partial
		}
		m.add(kindStatus, line)
		m.goalLive = nil
		m.refreshVP()
		return m.pollGoal()
	}
	switch ev.Kind {
	case goal.EventStart:
		m.add(kindStatus, fmt.Sprintf("goal · %s start · %s", st.ID, short(st.Goal, 48)))
	case goal.EventStep:
		m.add(kindStatus, fmt.Sprintf("goal · %s · step %d/%d", st.ID, st.Step, st.MaxSteps))
		if node := goalNodeLine(st); node != "" {
			m.add(kindStatus, node)
		}
		// Show the step's assistant reply in the transcript.
		if reply := strings.TrimSpace(st.LastReply); reply != "" {
			reply = stripGoalMarkers(reply)
			if reply != "" {
				m.add(kindAssistant, reply)
			}
		}
		// Clear live stream leftovers between goal steps (busy stays true).
		m.clearLiveStream()
	case goal.EventDone:
		// Capture reply before the status line so we can skip a duplicate of the
		// last step's assistant message (status would otherwise become "last").
		reply := strings.TrimSpace(stripGoalMarkers(st.LastReply))
		m.add(kindStatus, fmt.Sprintf("goal · %s done", st.ID))
		if reply != "" && !lastAssistantIs(m, reply) {
			m.add(kindAssistant, reply)
		}
		m.goalLive = nil
	case goal.EventFail:
		msg := st.Error
		if msg == "" {
			msg = ev.Text
		}
		m.add(kindError, fmt.Sprintf("goal · %s failed · %s", st.ID, msg))
		m.goalLive = nil
	}
	m.refreshVP()
	return m.pollGoal()
}

func stripGoalMarkers(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == goal.MarkerDone || strings.HasPrefix(t, goal.MarkerDone+" ") {
			continue
		}
		if strings.HasPrefix(t, goal.MarkerFailed) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// lastAssistantIs reports whether the most recent assistant transcript entry
// already holds exactly text (used to avoid double-painting the final reply).
func lastAssistantIs(m *model, text string) bool {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == kindAssistant {
			return m.entries[i].text == text
		}
	}
	return false
}

func lastEntryIsErrorContaining(m *model, sub string) bool {
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if e.kind == kindError && strings.Contains(e.text, sub) {
			return true
		}
		// Stop at non-status chrome once we leave the trailing error/status tail.
		if e.kind != kindError && e.kind != kindStatus {
			break
		}
	}
	return false
}

func (m *model) handleGoalSlash(parts []string) tea.Cmd {
	// parts[0] == "/goal"
	args := parts[1:]
	store := &goal.Store{}

	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	// bare /goal, list, ls, board → inventory (never index args[0] when empty)
	if sub == "" || sub == "list" || sub == "ls" || sub == "board" {
		list, err := store.List()
		if err != nil {
			m.add(kindError, "goal list: "+err.Error())
			m.refreshVP()
			return nil
		}
		if len(list) == 0 {
			m.add(kindStatus, "goal · (none) — /goal new <id> <text> · /goal <text>")
			m.refreshVP()
			return nil
		}
		var b strings.Builder
		if sub == "board" {
			b.WriteString("goal board\n")
			b.WriteString("  ID            STATUS    STEP   GOAL\n")
			b.WriteString("  ────────────  ────────  ─────  ────\n")
			for _, st := range list {
				fmt.Fprintf(&b, "  %-12s  %-8s  %2d/%-2d  %s\n",
					short(st.ID, 12), st.Status, st.Step, st.MaxSteps, short(st.Goal, 36))
			}
		} else {
			b.WriteString("goals\n")
			for _, st := range list {
				fmt.Fprintf(&b, "  %s  %s  %d/%d  %s\n",
					st.ID, st.Status, st.Step, st.MaxSteps, short(st.Goal, 40))
			}
		}
		b.WriteString("run: /goal run <id>   status: /goal status <id>   remove: /goal remove <id> [--force]   facts: /goal facts <id>   board: /goal board   worktrees: /goal worktrees")
		m.add(kindStatus, strings.TrimRight(b.String(), "\n"))
		m.refreshVP()
		return nil
	}

	switch args[0] {
	case "status":
		id := ""
		if len(args) > 1 {
			id = args[1]
		} else if m.goalLive != nil {
			id = m.goalLive.ID
		}
		if id == "" {
			m.add(kindError, "usage: /goal status <id>")
			m.refreshVP()
			return nil
		}
		st, err := store.Load(id)
		if err != nil {
			m.add(kindError, "goal status: "+err.Error())
			m.refreshVP()
			return nil
		}
		line := fmt.Sprintf("goal %s · %s · step %d/%d", st.ID, st.Status, st.Step, st.MaxSteps)
		if st.Error != "" {
			line += " · " + st.Error
		}
		m.add(kindStatus, line)
		if s := strings.TrimSpace(st.Summary); s != "" {
			m.add(kindStatus, "summary · "+short(s, 200))
		}
		if partial := strings.TrimSpace(st.Partial); partial != "" {
			m.add(kindStatus, "partial · "+partial)
		}
		if question := strings.TrimSpace(st.Question); question != "" {
			m.add(kindError, "goal · "+st.ID+" BLOCKED — "+question)
		}
		if len(st.Facts) > 0 {
			m.addGoalFacts(st)
		}
		m.refreshVP()
		return nil

	case "facts":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			m.add(kindError, "usage: /goal facts <id>")
			m.refreshVP()
			return nil
		}
		st, err := store.Load(args[1])
		if err != nil {
			m.add(kindError, "goal facts: "+err.Error())
			m.refreshVP()
			return nil
		}
		m.addGoalFacts(st)
		m.refreshVP()
		return nil

	case "nodes":
		id := ""
		if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
			id = args[1]
		} else if m.goalLive != nil {
			id = m.goalLive.ID
		}
		if id == "" {
			m.add(kindError, "usage: /goal nodes <id>")
			m.refreshVP()
			return nil
		}
		st, err := store.Load(id)
		if err != nil {
			m.add(kindError, "goal nodes: "+err.Error())
			m.refreshVP()
			return nil
		}
		m.addGoalNodes(st)
		m.refreshVP()
		return nil

	case "worktrees":
		m.goalWorktrees(args[1:])
		return nil

	case "new":
		if len(args) < 3 {
			m.add(kindError, "usage: /goal new <id> <goal text...>")
			m.refreshVP()
			return nil
		}
		id := args[1]
		text := strings.Join(args[2:], " ")
		r := &goal.Runner{Store: store}
		st, err := r.Create(goal.Spec{ID: id, Goal: text})
		if err != nil {
			m.add(kindError, "goal new: "+err.Error())
			m.refreshVP()
			return nil
		}
		m.add(kindStatus, fmt.Sprintf("goal · created %s · /goal run %s", st.ID, st.ID))
		m.refreshVP()
		return nil

	case "run":
		if len(args) < 2 {
			m.add(kindError, "usage: /goal run <id>")
			m.refreshVP()
			return nil
		}
		return m.startGoalRun(args[1], "", 0)

	case "remove", "delete", "rm":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			m.add(kindError, "usage: /goal remove <id> [--force]")
			m.refreshVP()
			return nil
		}
		id := strings.TrimSpace(args[1])
		force := false
		for _, a := range args[2:] {
			if a == "--force" || a == "-f" || a == "force" {
				force = true
			}
		}
		// Refuse to delete the goal currently driving this TUI session: its
		// runner is live in a goroutine and deleting the file under it leaves
		// a dangling run and a stale live chip.
		if !force && m.goalLive != nil && m.goalLive.ID == id && m.goalLive.Status == goal.StatusRunning {
			m.add(kindError, fmt.Sprintf("goal · %s is running in this session — cancel it or use /goal remove %s --force", id, id))
			m.refreshVP()
			return nil
		}
		store := &goal.Store{}
		blocked := false
		if !force {
			if st, lerr := store.Load(id); lerr == nil {
				blocked = st.Status == goal.StatusBlocked
			}
		}
		if err := store.Remove(id, force); err != nil {
			cause := err.Error()
			if errors.Is(err, goal.ErrGoalRunning) {
				cause = fmt.Sprintf("goal · %s is running — stop it (Ctrl-C / /goal run resume finishes) or use /goal remove %s --force", id, id)
			} else if errors.Is(err, goal.ErrGoalNotFound) {
				cause = fmt.Sprintf("goal · %s not found — /goal list shows ids", id)
			}
			m.add(kindError, "goal remove: "+cause)
			m.refreshVP()
			return nil
		}
		// Clear the live chip if we just deleted the goal it tracks.
		if m.goalLive != nil && m.goalLive.ID == id {
			m.goalLive = nil
		}
		m.add(kindStatus, fmt.Sprintf("goal · deleted %s", id))
		// Blocked runs may have kept a worktree on disk for inspection; it is
		// separate from the goal record, so point at the prune affordance.
		if blocked {
			m.add(kindStatus, fmt.Sprintf("goal · %s was blocked — leftover worktrees are separate: /goal worktrees prune", id))
		}
		m.refreshVP()
		return nil
	}

	// /goal <text...> — one-shot RunSpec
	text := strings.Join(args, " ")
	return m.startGoalRun("", text, 0)
}

// goalNodeLine renders the one-line current-node status for a step event:
// "goal · <id> · node 2/5 [b] verify-pricing". Empty when the goal has no
// checklist (Nodes() then yields only the synthetic goal node, which adds
// nothing over the step line).
func goalNodeLine(st goal.State) string {
	if !st.Plan.HasItems() {
		return ""
	}
	nodes := st.Nodes()
	idx := -1
	if st.CurrentItem != "" {
		for i := range nodes {
			if nodes[i].ID == st.CurrentItem {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		for i := range nodes {
			if nodes[i].Status != string(goal.ItemDone) &&
				nodes[i].Status != string(goal.ItemFailed) &&
				nodes[i].Status != string(goal.ItemSkipped) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		if sum := strings.TrimSpace(st.NodeSummary()); sum != "" {
			return fmt.Sprintf("goal · %s · %s", st.ID, sum)
		}
		return ""
	}
	n := nodes[idx]
	title := strings.Join(strings.Fields(n.Title), " ")
	if title == "" {
		title = n.ID
	}
	return fmt.Sprintf("goal · %s · node %d/%d [%s] %s", st.ID, idx+1, len(nodes), n.ID, short(title, 40))
}

// addGoalNodes lists the plan checklist (id · title · status) as quiet
// transcript lines, mirroring /goal facts.
func (m *model) addGoalNodes(st goal.State) {
	if !st.Plan.HasItems() {
		m.add(kindStatus, "nodes · none")
		return
	}
	for _, n := range st.Nodes() {
		title := strings.Join(strings.Fields(n.Title), " ")
		m.add(kindStatus, fmt.Sprintf("nodes · %s · %s · %s", n.ID, n.Status, short(title, 60)))
	}
}

// addGoalFacts prints the durable evidence ledger as quiet transcript lines.
func (m *model) addGoalFacts(st goal.State) {
	if facts := strings.TrimSpace(st.FactsText()); facts != "" {
		for _, fact := range strings.Split(facts, "\n") {
			m.add(kindStatus, "facts · "+fact)
		}
		return
	}
	m.add(kindStatus, "facts · none")
}

func (m *model) startGoalRun(id, goalText string, maxSteps int) tea.Cmd {
	if m.busy {
		m.add(kindError, "busy — finish or cancel the current turn first")
		m.refreshVP()
		return nil
	}
	if m.eng == nil {
		m.add(kindError, "goal: no engine")
		m.refreshVP()
		return nil
	}
	m.showWelcome = false
	m.resetStreamState()
	m.resetToolTally()
	// Token baseline is managed per-goal in handleGoalEvent — do not reset
	// here (State.InputTokens is cumulative across runs of a goal).
	m.busy = true
	m.followBottom = true
	m.startedAt = time.Now()
	m.syncInputChrome()
	m.layout()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	// Stream tokens from each goal step into the live frame.
	ing := newStreamIngest()
	m.ingest = ing
	if m.stream {
		m.eng.SetOnToken(ing.pushContent)
		m.eng.SetOnReasoning(ing.pushReasoning)
	}

	id = strings.TrimSpace(id)
	goalText = strings.TrimSpace(goalText)
	if id != "" {
		m.add(kindStatus, "goal · running "+id)
		// Header chip before the first Subscribe event arrives.
		m.goalLive = &goal.State{ID: id, Status: goal.StatusRunning}
	} else {
		m.add(kindStatus, "goal · running · "+short(goalText, 48))
		// One-shot: chip uses a short label until Runner assigns an id.
		m.goalLive = &goal.State{ID: short(goalText, 16), Status: goal.StatusRunning, Goal: goalText}
	}
	m.refreshVP()

	return tea.Batch(
		m.scheduleBusyHeartbeat(),
		m.pollStream(),
		m.pollGoal(),
		func() tea.Msg {
			r := &goal.Runner{Engine: m.eng, Store: &goal.Store{}}
			var st goal.State
			var err error
			if goalText != "" {
				st, err = r.RunSpec(ctx, goal.Spec{ID: id, Goal: goalText, MaxSteps: maxSteps})
			} else {
				st, err = r.Run(ctx, id)
			}
			m.eng.SetOnToken(nil)
			m.eng.SetOnReasoning(nil)
			ing.finish()
			return goalDoneMsg{state: st, err: err}
		},
	)
}

func (m *model) handleGoalDone(msg goalDoneMsg) tea.Cmd {
	m.busy = false
	m.cancel = nil
	m.toolCurrent = ""
	m.eng.SetOnToken(nil)
	m.eng.SetOnReasoning(nil)
	if m.ingest != nil {
		c, r, _ := m.ingest.take()
		m.applyStreamSnap(c, r)
		m.ingest = nil
	}
	m.resetStreamState()
	m.goalLive = nil
	if msg.err != nil && msg.state.Status != goal.StatusDone {
		// EventFail usually already logged; ensure a line if the bus dropped it
		// or the runner failed before emitting.
		errText := msg.state.Error
		if errText == "" {
			errText = msg.err.Error()
		}
		id := msg.state.ID
		if id == "" && m.goalLive != nil {
			id = m.goalLive.ID
		}
		prefix := "goal · "
		if id != "" {
			prefix = fmt.Sprintf("goal · %s · ", id)
		}
		want := prefix + errText
		if !lastEntryIsErrorContaining(m, errText) {
			m.add(kindError, want)
		}
	}
	m.syncInputChrome()
	m.layout()
	m.refreshVP()
	if len(m.queued) > 0 {
		if _, cmd := m.dequeue(); cmd != nil {
			return tea.Batch(m.pollPerm(), m.pollToolUI(), m.pollGoal(), cmd)
		}
	}
	return tea.Batch(m.pollPerm(), m.pollToolUI(), m.pollGoal())
}

func goalHeaderChip(st *goal.State) string {
	if st == nil {
		return ""
	}
	return fmt.Sprintf("goal %s %d/%d", st.ID, st.Step, st.MaxSteps)
}

// goalListWorktrees / goalGit are package vars so tests can stub the git
// boundary — tests must not shell out to real git.
var (
	goalListWorktrees = goal.ListWorktrees
	goalGit           = runGoalGit
)

// runGoalGit runs one git command in dir. Prune uses the same commands as
// mow's worktree cleanup(): remove the checkout, then delete the branch.
func runGoalGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// goalWorktreeConflict reports whether a blocked goal's question describes a
// kept worktree merge conflict. The runner derives the question from the
// escalate summary "merge conflict in <item> — worktree kept at <dir>", so a
// substring match tracks mow's wording.
func goalWorktreeConflict(question string) bool {
	return strings.Contains(strings.ToLower(question), "merge conflict")
}

// goalWorktrees implements "/goal worktrees [prune]": list the mow-wt-*
// worktrees a conflicted run left behind, or explicitly remove them. Pruning
// is opt-in only — nothing here ever removes a worktree silently.
func (m *model) goalWorktrees(args []string) {
	prune := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "prune":
		prune = true
	default:
		m.add(kindError, "usage: /goal worktrees [prune]")
		m.refreshVP()
		return
	}
	if m.eng == nil || strings.TrimSpace(m.eng.Workspace()) == "" {
		m.add(kindError, "goal worktrees: no engine workspace")
		m.refreshVP()
		return
	}
	m.goalWorktreesIn(m.eng.Workspace(), prune)
}

// goalWorktreesIn is the testable core: list or prune worktrees under ws.
func (m *model) goalWorktreesIn(ws string, prune bool) {
	ctx := context.Background()
	wts, err := goalListWorktrees(ctx, ws)
	if err != nil {
		m.add(kindError, "goal worktrees: "+err.Error())
		m.refreshVP()
		return
	}
	if len(wts) == 0 {
		m.add(kindStatus, "worktrees · none")
		m.refreshVP()
		return
	}
	if !prune {
		for _, wt := range wts {
			m.add(kindStatus, fmt.Sprintf("worktree · %s · %s", wt.Branch, wt.Dir))
		}
		m.add(kindStatus, "worktrees · remove with /goal worktrees prune")
		m.refreshVP()
		return
	}
	removed := 0
	var failed []string
	for _, wt := range wts {
		if err := goalGit(ctx, ws, "worktree", "remove", "--force", wt.Dir); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", wt.Branch, err))
			continue
		}
		if err := goalGit(ctx, ws, "branch", "-D", wt.Branch); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", wt.Branch, err))
			continue
		}
		removed++
	}
	if len(failed) > 0 {
		for _, f := range failed {
			m.add(kindError, "prune · failed "+f)
		}
	} else {
		m.add(kindStatus, fmt.Sprintf("prune · removed %d worktree(s)", removed))
	}
	m.refreshVP()
}
