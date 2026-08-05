package mowi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/subosito/mow/packs/goal"
)

// goalTestModel returns a ready model with isolated $MOW_HOME for goal store.
func goalTestModel(t *testing.T) *model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 100, 40
	m.layout()
	m.ready = true
	m.showWelcome = false
	return m
}

func entryTexts(m *model) []string {
	out := make([]string, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.text
	}
	return out
}

func lastEntryContains(m *model, sub string) bool {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if strings.Contains(m.entries[i].text, sub) {
			return true
		}
	}
	return false
}

func findEntry(m *model, kind entryKind, sub string) bool {
	for _, e := range m.entries {
		if e.kind == kind && strings.Contains(e.text, sub) {
			return true
		}
	}
	return false
}

func drainCmd(t *testing.T, cmd tea.Cmd, timeout time.Duration) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatalf("cmd timed out after %s", timeout)
		return nil
	}
}

// ---------- stripGoalMarkers ----------

func TestStripGoalMarkers(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"hello\nGOAL_DONE\n", "hello"},
		{"GOAL_DONE", ""},
		{"GOAL_DONE trailing ignored as prefix match", ""},
		{"ok\nGOAL_DONE more", "ok"},
		{"fail\nGOAL_FAILED: boom\n", "fail"},
		{"GOAL_FAILED:", ""},
		{"GOAL_FAILED: reason here", ""},
		{"a\nGOAL_DONE\nb\nGOAL_FAILED: x\nc", "a\nb\nc"},
		{"  GOAL_DONE  \nkeep", "keep"},
		{"", ""},
		{"line with GOAL_DONE mid-sentence stays", "line with GOAL_DONE mid-sentence stays"},
	}
	for _, tc := range cases {
		got := stripGoalMarkers(tc.in)
		if got != tc.want {
			t.Errorf("stripGoalMarkers(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// ---------- goalHeaderChip ----------

func TestGoalHeaderChip(t *testing.T) {
	if got := goalHeaderChip(nil); got != "" {
		t.Fatalf("nil → %q", got)
	}
	st := &goal.State{ID: "ship-x", Step: 3, MaxSteps: 8}
	got := goalHeaderChip(st)
	if got != "goal ship-x 3/8" {
		t.Fatalf("got %q", got)
	}
}

// ---------- handleGoalEvent ----------

func TestHandleGoalEventStart(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	cmd := m.handleGoalEvent(goal.Event{
		Kind: goal.EventStart,
		State: goal.State{
			ID: "g1", Goal: "ship feature X", Status: goal.StatusRunning, MaxSteps: 8,
		},
	})
	if !findEntry(m, kindStatus, "goal · g1 start") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !findEntry(m, kindStatus, "ship feature X") {
		t.Fatalf("want goal text in start line: %+v", entryTexts(m))
	}
	if m.goalLive == nil || m.goalLive.ID != "g1" {
		t.Fatalf("goalLive=%v", m.goalLive)
	}
	if cmd == nil {
		t.Fatal("expected pollGoal cmd")
	}
}

func TestHandleGoalEventStepAddsReplyAndStripsMarkers(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventStep,
		State: goal.State{
			ID: "g1", Status: goal.StatusRunning, Step: 2, MaxSteps: 8,
			LastReply:   "progress update\nGOAL_DONE\n",
			InputTokens: 10, OutputTokens: 5,
		},
	})
	if !findEntry(m, kindStatus, "step 2/8") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !findEntry(m, kindAssistant, "progress update") {
		t.Fatalf("want assistant reply: %+v", entryTexts(m))
	}
	for _, e := range m.entries {
		if strings.Contains(e.text, "GOAL_DONE") {
			t.Fatalf("marker leaked: %q", e.text)
		}
	}
	if m.tokIn != 10 || m.tokOut != 5 {
		t.Fatalf("tokens in=%d out=%d", m.tokIn, m.tokOut)
	}
	if m.goalLive == nil {
		t.Fatal("expected goalLive while running")
	}
}

func TestHandleGoalEventPartialAndBlocked(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.goalLive = &goal.State{ID: "g1", Status: goal.StatusRunning}
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventPartial,
		State: goal.State{ID: "g1", Status: goal.StatusPartial, Partial: "done 2/3 items, 1 missing"},
	})
	if !findEntry(m, kindStatus, "goal · g1 partial · done 2/3 items, 1 missing") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if m.goalLive != nil {
		t.Fatal("partial goal should clear live state")
	}

	m.goalLive = &goal.State{ID: "g1", Status: goal.StatusRunning}
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventBlocked,
		State: goal.State{ID: "g1", Status: goal.StatusBlocked, Question: "Should this change be deployed?"},
	})
	if !findEntry(m, kindError, "goal · g1 BLOCKED — Should this change be deployed?") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if m.goalLive != nil {
		t.Fatal("blocked goal should clear live state")
	}
}

func TestHandleGoalEventStepTokenDelta(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventStep,
		State: goal.State{ID: "g1", Status: goal.StatusRunning, Step: 1, MaxSteps: 4, InputTokens: 100, OutputTokens: 40},
	})
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventStep,
		State: goal.State{ID: "g1", Status: goal.StatusRunning, Step: 2, MaxSteps: 4, InputTokens: 150, OutputTokens: 70},
	})
	if m.tokIn != 150 || m.tokOut != 70 {
		t.Fatalf("tokIn=%d tokOut=%d want 150/70", m.tokIn, m.tokOut)
	}
	if m.goalTokIn != 150 || m.goalTokOut != 70 {
		t.Fatalf("goalTok trackers in=%d out=%d", m.goalTokIn, m.goalTokOut)
	}
}

func TestHandleGoalEventDone(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.add(kindAssistant, "final answer")
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventDone,
		State: goal.State{
			ID: "g1", Status: goal.StatusDone, Step: 3, MaxSteps: 8,
			LastReply: "final answer",
		},
	})
	if !findEntry(m, kindStatus, "goal · g1 done") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	n := 0
	for _, e := range m.entries {
		if e.kind == kindAssistant && e.text == "final answer" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("assistant entries with final answer: %d want 1; %+v", n, entryTexts(m))
	}
	if m.goalLive != nil {
		t.Fatal("goalLive should clear on done")
	}
}

func TestHandleGoalEventDoneAddsNewReply(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventDone,
		State: goal.State{
			ID: "g2", Status: goal.StatusDone, LastReply: "shipped\nGOAL_DONE",
		},
	})
	if !findEntry(m, kindAssistant, "shipped") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if m.goalLive != nil {
		t.Fatal("goalLive should clear")
	}
}

func TestHandleGoalEventFail(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventFail,
		State: goal.State{
			ID: "g1", Status: goal.StatusFailed, Error: "boom",
		},
	})
	if !findEntry(m, kindError, "goal · g1 failed") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !findEntry(m, kindError, "boom") {
		t.Fatalf("want error text: %+v", entryTexts(m))
	}
	if m.goalLive != nil {
		t.Fatal("goalLive should clear on fail")
	}
}

func TestHandleGoalEventFailUsesEventText(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventFail,
		Text:  "from-event",
		State: goal.State{ID: "g1", Status: goal.StatusFailed},
	})
	if !findEntry(m, kindError, "from-event") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalEventMsgViaUpdate(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	mod, cmd := m.Update(goalEventMsg{ev: goal.Event{
		Kind:  goal.EventStart,
		State: goal.State{ID: "via-upd", Goal: "from update", Status: goal.StatusRunning, MaxSteps: 4},
	}})
	tm := mod.(*model)
	if !findEntry(tm, kindStatus, "via-upd") {
		t.Fatalf("entries: %+v", entryTexts(tm))
	}
	if cmd == nil {
		t.Fatal("expected continuing poll cmd")
	}
}

// ---------- /goal slash ----------

func TestGoalSlashEmptyList(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleSlash("/goal")
	if !lastEntryContains(m, "goal · (none)") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashListAliases(t *testing.T) {
	m := goalTestModel(t)
	store := &goal.Store{}
	if _, err := (&goal.Runner{Store: store}).Create(goal.Spec{ID: "alpha", Goal: "do alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&goal.Runner{Store: store}).Create(goal.Spec{ID: "beta", Goal: "do beta"}); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"/goal", "/goal list", "/goal ls"} {
		m.entries = nil
		_ = m.handleSlash(cmd)
		if !lastEntryContains(m, "alpha") || !lastEntryContains(m, "beta") {
			t.Fatalf("%s entries: %+v", cmd, entryTexts(m))
		}
		if !lastEntryContains(m, "goals") {
			t.Fatalf("%s want 'goals' header: %+v", cmd, entryTexts(m))
		}
	}

	m.entries = nil
	_ = m.handleSlash("/goal board")
	if !lastEntryContains(m, "goal board") {
		t.Fatalf("board: %+v", entryTexts(m))
	}
	if !lastEntryContains(m, "STATUS") {
		t.Fatalf("board header: %+v", entryTexts(m))
	}
	if !lastEntryContains(m, "alpha") {
		t.Fatalf("board rows: %+v", entryTexts(m))
	}
}

func TestGoalSlashNew(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleSlash("/goal new ship-it write the tests")
	if !lastEntryContains(m, "goal · created ship-it") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !lastEntryContains(m, "/goal run ship-it") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	st, err := (&goal.Store{}).Load("ship-it")
	if err != nil {
		t.Fatal(err)
	}
	if st.Goal != "write the tests" || st.Status != goal.StatusPending {
		t.Fatalf("stored: %+v", st)
	}
}

func TestGoalSlashNewUsage(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleSlash("/goal new")
	if !lastEntryContains(m, "usage: /goal new") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	m.entries = nil
	_ = m.handleSlash("/goal new onlyid")
	if !lastEntryContains(m, "usage: /goal new") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashStatus(t *testing.T) {
	m := goalTestModel(t)
	store := &goal.Store{}
	st, err := (&goal.Runner{Store: store}).Create(goal.Spec{ID: "stat1", Goal: "check status"})
	if err != nil {
		t.Fatal(err)
	}
	st.Status = goal.StatusRunning
	st.Step = 2
	st.Summary = "halfway there with lots of detail " + strings.Repeat("x", 50)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}

	_ = m.handleSlash("/goal status stat1")
	if !lastEntryContains(m, "goal stat1") || !lastEntryContains(m, "running") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !lastEntryContains(m, "step 2/") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !lastEntryContains(m, "summary ·") {
		t.Fatalf("want summary line: %+v", entryTexts(m))
	}
}

func TestGoalSlashFactsAndStatusEvidence(t *testing.T) {
	m := goalTestModel(t)
	store := &goal.Store{}
	st, err := (&goal.Runner{Store: store}).Create(goal.Spec{ID: "evidence", Goal: "collect proof"})
	if err != nil {
		t.Fatal(err)
	}
	st.Status = goal.StatusPartial
	st.Partial = "done 2/3 items, 1 missing"
	st.Facts = []goal.Fact{{Claim: "tests passed", Source: "go test ./...", Confidence: 0.9}}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}

	_ = m.handleSlash("/goal facts evidence")
	if !findEntry(m, kindStatus, "facts · - tests passed (source: go test ./...) [90%]") {
		t.Fatalf("facts entries: %+v", entryTexts(m))
	}
	m.entries = nil
	_ = m.handleSlash("/goal status evidence")
	if !findEntry(m, kindStatus, "partial · done 2/3 items, 1 missing") ||
		!findEntry(m, kindStatus, "facts · - tests passed") {
		t.Fatalf("status entries: %+v", entryTexts(m))
	}

	m.entries = nil
	st.Facts = nil
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	_ = m.handleSlash("/goal facts evidence")
	if !findEntry(m, kindStatus, "facts · none") {
		t.Fatalf("empty facts entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashStatusBlocked(t *testing.T) {
	m := goalTestModel(t)
	store := &goal.Store{}
	st, err := (&goal.Runner{Store: store}).Create(goal.Spec{ID: "blocked", Goal: "decide"})
	if err != nil {
		t.Fatal(err)
	}
	st.Status = goal.StatusBlocked
	st.Question = "Should this change be deployed?"
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	_ = m.handleSlash("/goal status blocked")
	if !findEntry(m, kindError, "goal · blocked BLOCKED — Should this change be deployed?") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashStatusFromLive(t *testing.T) {
	m := goalTestModel(t)
	store := &goal.Store{}
	if _, err := (&goal.Runner{Store: store}).Create(goal.Spec{ID: "live1", Goal: "live goal"}); err != nil {
		t.Fatal(err)
	}
	m.goalLive = &goal.State{ID: "live1"}
	_ = m.handleSlash("/goal status")
	if !lastEntryContains(m, "goal live1") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashStatusUsage(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleSlash("/goal status")
	if !lastEntryContains(m, "usage: /goal status") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashStatusMissing(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleSlash("/goal status no-such-goal")
	if !lastEntryContains(m, "goal status:") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashRunUsage(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleSlash("/goal run")
	if !lastEntryContains(m, "usage: /goal run") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashRunBusy(t *testing.T) {
	m := goalTestModel(t)
	m.busy = true
	_ = m.handleSlash("/goal run anything")
	if !lastEntryContains(m, "busy") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashRunNoEngine(t *testing.T) {
	m := goalTestModel(t)
	m.eng = nil
	_ = m.handleSlash("/goal run x")
	if !lastEntryContains(m, "no engine") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

func TestGoalSlashRunStartsBusy(t *testing.T) {
	m := goalTestModel(t)
	// Create a pending goal then run it — cancel immediately so the runner exits.
	if _, err := (&goal.Runner{Store: &goal.Store{}}).Create(goal.Spec{ID: "runme", Goal: "run me"}); err != nil {
		t.Fatal(err)
	}
	cmd := m.handleSlash("/goal run runme")
	if cmd == nil {
		t.Fatal("expected run cmd batch")
	}
	if !m.busy {
		t.Fatal("expected busy during goal run")
	}
	if !lastEntryContains(m, "goal · running runme") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if m.cancel == nil {
		t.Fatal("expected cancel func")
	}
	// Cancel so the background runner does not leak past the test.
	m.cancel()
	// Drain goalDoneMsg from the batch (runner cmd returns it; others may block).
	deadline := time.Now().Add(3 * time.Second)
	pending := []tea.Cmd{cmd}
	var sawDone bool
	for !sawDone && time.Now().Before(deadline) && len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		if c == nil {
			continue
		}
		msgCh := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { msgCh <- c() }(c)
		select {
		case msg := <-msgCh:
			switch v := msg.(type) {
			case goalDoneMsg:
				sawDone = true
				mod, _ := m.Update(v)
				m = mod.(*model)
			case tea.BatchMsg:
				for _, sub := range v {
					pending = append(pending, sub)
				}
			default:
				// Ignore heartbeat/stream/poll timeouts — they block on channels.
			}
		case <-time.After(400 * time.Millisecond):
			// Likely a blocking poll; skip.
			continue
		}
	}
	if m.busy && sawDone {
		t.Fatal("busy should clear after goalDoneMsg")
	}
	// Ensure cancel fired even if we didn't drain done.
	if m.cancel != nil {
		m.cancel()
	}
}

func TestGoalSlashOneshootStarts(t *testing.T) {
	m := goalTestModel(t)
	cmd := m.handleSlash("/goal finish the audit feature")
	if cmd == nil {
		t.Fatal("expected run cmd")
	}
	if !m.busy {
		t.Fatal("expected busy")
	}
	if !lastEntryContains(m, "goal · running") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !lastEntryContains(m, "finish the audit") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func TestGoalSlashViaSubmit(t *testing.T) {
	m := goalTestModel(t)
	m.ta.SetValue("/goal")
	mod, _ := m.submit()
	tm := mod.(*model)
	if !lastEntryContains(tm, "goal · (none)") {
		t.Fatalf("entries: %+v", entryTexts(tm))
	}
}

func TestHelpMentionsGoal(t *testing.T) {
	m := goalTestModel(t)
	m.showHelp = true
	out := m.View()
	// View returns tea.View — string content via String if available.
	s := viewString(out)
	if !strings.Contains(s, "/goal") {
		t.Fatalf("help view should mention /goal; got %q", short(s, 300))
	}
}

func viewString(v tea.View) string {
	// tea.View is a struct; Content is the main body in v2.
	// Fall back to fmt via the public fields we know about.
	type stringer interface{ String() string }
	if s, ok := any(v).(stringer); ok {
		return s.String()
	}
	// Best-effort: many BT versions put body in Content.
	return v.Content
}

// ---------- subscribe / pollGoal bus ----------

func TestInitGoalBusSubscribe(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	if m.goalCh == nil {
		t.Fatal("goalCh nil")
	}
	if m.goalUnsub == nil {
		t.Fatal("goalUnsub nil")
	}
	// Idempotent.
	ch1 := m.goalCh
	m.initGoalBus()
	if m.goalCh != ch1 {
		t.Fatal("initGoalBus must not replace existing bus")
	}

	go func() {
		m.goalCh <- goal.Event{
			Kind:  goal.EventStart,
			State: goal.State{ID: "bus1", Goal: "via bus", Status: goal.StatusRunning, MaxSteps: 4},
		}
	}()
	msg := drainCmd(t, m.pollGoal(), 2*time.Second)
	gem, ok := msg.(goalEventMsg)
	if !ok {
		t.Fatalf("got %T", msg)
	}
	if gem.ev.State.ID != "bus1" {
		t.Fatalf("ev=%+v", gem.ev)
	}
	mod, _ := m.Update(gem)
	tm := mod.(*model)
	if !findEntry(tm, kindStatus, "bus1") {
		t.Fatalf("entries: %+v", entryTexts(tm))
	}
}

func TestGoalBusNonBlockingDrop(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	dropped := false
	for i := 0; i < 40; i++ {
		select {
		case m.goalCh <- goal.Event{Kind: goal.EventStep, Text: "x"}:
		default:
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("expected drop when UI behind")
	}
}

func TestGoalUnsubSafe(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.goalUnsub()
	m.goalUnsub = nil // RunOpts nils after call
}

func TestNewModelInitWiresGoalBus(t *testing.T) {
	m := goalTestModel(t)
	cmd := m.Init()
	if m.goalCh == nil || m.goalUnsub == nil {
		t.Fatal("Init should wire goal bus")
	}
	if cmd == nil {
		t.Fatal("expected init batch cmd")
	}
	if m.goalUnsub != nil {
		m.goalUnsub()
	}
}

// ---------- handleGoalDone ----------

func TestHandleGoalDoneClearsBusy(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.busy = true
	m.goalLive = &goal.State{ID: "x"}
	m.startedAt = time.Now()
	cmd := m.handleGoalDone(goalDoneMsg{
		state: goal.State{ID: "x", Status: goal.StatusDone},
	})
	if m.busy {
		t.Fatal("busy")
	}
	if m.goalLive != nil {
		t.Fatal("goalLive")
	}
	if cmd == nil {
		t.Fatal("expected poll batch")
	}
}

func TestHandleGoalDoneWithError(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.busy = true
	_ = m.handleGoalDone(goalDoneMsg{
		state: goal.State{ID: "x", Status: goal.StatusFailed},
		err:   errors.New("boom-goal"),
	})
	if !findEntry(m, kindError, "goal ·") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !findEntry(m, kindError, "boom-goal") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if m.busy {
		t.Fatal("busy should clear")
	}
}

func TestHandleGoalDoneDrainsQueue(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.busy = true
	m.queued = []string{"next please"}
	_ = m.handleGoalDone(goalDoneMsg{
		state: goal.State{ID: "x", Status: goal.StatusDone},
	})
	// Queue drained into a new turn (busy again) or emptied.
	if len(m.queued) > 0 {
		t.Fatalf("queued not drained: %v", m.queued)
	}
}

// ---------- status line includes goal chip ----------

func TestStatusIncludesGoalChip(t *testing.T) {
	m := goalTestModel(t)
	m.goalLive = &goal.State{ID: "chip1", Step: 1, MaxSteps: 8}
	_ = m.handleSlash("/status")
	if !lastEntryContains(m, "goal chip1 1/8") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

// ---------- store isolation ----------

func TestGoalStoreFileWritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	_ = m.handleSlash("/goal new filecheck hello world goal")
	p := filepath.Join(dir, "goals", "filecheck.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("missing %s: %v", p, err)
	}
}

// ---------- package Subscribe → channel → UI ----------

func TestGoalPackageSubscribeDelivers(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()

	// Fire through the real package Subscribe path used by runners.
	// We can't call emit() (unexported), but our initGoalBus already
	// registered via goal.Subscribe — send on our channel and also verify
	// the unsubscribe removes cleanly.
	unsub := m.goalUnsub
	if unsub == nil {
		t.Fatal("nil unsub")
	}

	// Direct channel path (same as Subscribe callback body).
	select {
	case m.goalCh <- goal.Event{Kind: goal.EventFail, State: goal.State{ID: "sub1", Error: "nope"}}:
	default:
		t.Fatal("channel full")
	}
	msg := drainCmd(t, m.pollGoal(), time.Second)
	gem, ok := msg.(goalEventMsg)
	if !ok || gem.ev.State.ID != "sub1" {
		t.Fatalf("msg=%T %+v", msg, msg)
	}
	mod, _ := m.Update(gem)
	tm := mod.(*model)
	if !findEntry(tm, kindError, "sub1") {
		t.Fatalf("entries: %+v", entryTexts(tm))
	}
	unsub()
}

func TestLastAssistantIs(t *testing.T) {
	m := goalTestModel(t)
	if lastAssistantIs(m, "x") {
		t.Fatal("empty")
	}
	m.add(kindAssistant, "hello")
	m.add(kindStatus, "noise")
	if !lastAssistantIs(m, "hello") {
		t.Fatal("should find last assistant behind status")
	}
	if lastAssistantIs(m, "other") {
		t.Fatal("mismatch")
	}
}

func TestHandleGoalEventDoneSkipsDuplicateReply(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventStep,
		State: goal.State{
			ID: "dup", Status: goal.StatusRunning, Step: 1, MaxSteps: 2,
			LastReply: "same final",
		},
	})
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventDone,
		State: goal.State{
			ID: "dup", Status: goal.StatusDone, Step: 1, MaxSteps: 2,
			LastReply: "same final",
		},
	})
	n := 0
	for _, e := range m.entries {
		if e.kind == kindAssistant && e.text == "same final" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("assistant copies=%d want 1; %+v", n, entryTexts(m))
	}
}

func TestStartGoalRunSetsLiveChip(t *testing.T) {
	m := goalTestModel(t)
	if _, err := (&goal.Runner{Store: &goal.Store{}}).Create(goal.Spec{ID: "chiprun", Goal: "c"}); err != nil {
		t.Fatal(err)
	}
	cmd := m.startGoalRun("chiprun", "", 0)
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if m.goalLive == nil || m.goalLive.ID != "chiprun" {
		t.Fatalf("goalLive=%v", m.goalLive)
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func TestHandleGoalDoneSurfacesErrorWithoutPriorEvent(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	m.busy = true
	_ = m.handleGoalDone(goalDoneMsg{
		state: goal.State{ID: "e1", Status: goal.StatusFailed},
		err:   errors.New("runner exploded"),
	})
	if !findEntry(m, kindError, "runner exploded") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	// Second time with same error already present should not require panic;
	// simulate EventFail already logged:
	m2 := goalTestModel(t)
	m2.initGoalBus()
	m2.busy = true
	m2.add(kindError, "goal · e2 failed · already")
	_ = m2.handleGoalDone(goalDoneMsg{
		state: goal.State{ID: "e2", Status: goal.StatusFailed, Error: "already"},
		err:   errors.New("already"),
	})
	n := 0
	for _, e := range m2.entries {
		if e.kind == kindError && strings.Contains(e.text, "already") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("error lines=%d want 1; %+v", n, entryTexts(m2))
	}
}

func TestHandleGoalSlashUnknownSubFallsToOneshoot(t *testing.T) {
	// "/goal something words" is one-shot text, not an error for unknown subcommand
	// (except reserved: list/ls/board/status/new/run).
	m := goalTestModel(t)
	cmd := m.handleSlash("/goal ship the feature please")
	if cmd == nil {
		t.Fatal("expected start cmd")
	}
	if !m.busy {
		t.Fatal("busy")
	}
	if m.cancel != nil {
		m.cancel()
	}
}

// ---------- P3.5 node status ----------

func goalPlanState() goal.State {
	return goal.State{
		ID: "g1", Goal: "ship pricing", Status: goal.StatusRunning, Step: 2, MaxSteps: 8,
		CurrentItem: "b",
		Plan: goal.Plan{Items: []goal.PlanItem{
			{ID: "a", Title: "draft pricing", Status: goal.ItemDone},
			{ID: "b", Title: "verify pricing", Status: goal.ItemPending},
			{ID: "c", Title: "ship it", Status: goal.ItemPending},
		}},
	}
}

func TestGoalNodeLineCurrentItem(t *testing.T) {
	line := goalNodeLine(goalPlanState())
	if !strings.Contains(line, "goal · g1 · node 2/3 [b] verify pricing") {
		t.Fatalf("node line: %q", line)
	}
}

func TestGoalNodeLineFallsBackToNextPending(t *testing.T) {
	st := goalPlanState()
	st.CurrentItem = "" // hint cleared → first non-terminal node wins
	line := goalNodeLine(st)
	if !strings.Contains(line, "node 2/3 [b]") {
		t.Fatalf("fallback node line: %q", line)
	}
}

func TestGoalNodeLineSilentWithoutPlan(t *testing.T) {
	st := goal.State{ID: "g1", Status: goal.StatusRunning, Step: 1, MaxSteps: 4}
	if line := goalNodeLine(st); line != "" {
		t.Fatalf("plan-less goal must not emit a node line: %q", line)
	}
}

func TestHandleGoalEventStepRendersNodeLine(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventStep,
		State: goalPlanState(),
	})
	if !findEntry(m, kindStatus, "step 2/8") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
	if !findEntry(m, kindStatus, "node 2/3 [b] verify pricing") {
		t.Fatalf("want current node line: %+v", entryTexts(m))
	}
}

func TestGoalSlashNodes(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	r := &goal.Runner{Store: &goal.Store{}}
	st := goalPlanState()
	if err := r.Store.Save(st); err != nil {
		t.Fatal(err)
	}
	_ = m.handleGoalSlash([]string{"/goal", "nodes", "g1"})
	for _, want := range []string{
		"nodes · a · done · draft pricing",
		"nodes · b · pending · verify pricing",
		"nodes · c · pending · ship it",
	} {
		if !findEntry(m, kindStatus, want) {
			t.Fatalf("want %q: %+v", want, entryTexts(m))
		}
	}
}

func TestGoalSlashNodesNone(t *testing.T) {
	m := goalTestModel(t)
	m.initGoalBus()
	r := &goal.Runner{Store: &goal.Store{}}
	if err := r.Store.Save(goal.State{ID: "g2", Goal: "free-form", Status: goal.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	_ = m.handleGoalSlash([]string{"/goal", "nodes", "g2"})
	if !findEntry(m, kindStatus, "nodes · none") {
		t.Fatalf("entries: %+v", entryTexts(m))
	}
}

// ---------- /goal worktrees ----------

// stubWorktrees swaps the git boundary (goalListWorktrees + goalGit) for the
// duration of one test.
func stubWorktrees(t *testing.T, list func(context.Context, string) ([]goal.WorktreeInfo, error), git func(context.Context, string, ...string) error) {
	t.Helper()
	oldList, oldGit := goalListWorktrees, goalGit
	goalListWorktrees = list
	goalGit = git
	t.Cleanup(func() { goalListWorktrees, goalGit = oldList, oldGit })
}

func TestGoalWorktreesNone(t *testing.T) {
	m := goalTestModel(t)
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		return nil, nil
	}, nil)

	m.goalWorktreesIn(t.TempDir(), false)

	found := findEntry(m, kindStatus, "worktrees · none")
	if !found {
		t.Fatalf("want 'worktrees · none', got %v", entryTexts(m))
	}
}

func TestGoalWorktreesList(t *testing.T) {
	m := goalTestModel(t)
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		return []goal.WorktreeInfo{
			{Dir: "/tmp/mow-wt-1/wt", Branch: "mow-wt-g1-a", GoalID: "g1", ItemID: "a"},
			{Dir: "/tmp/mow-wt-2/wt", Branch: "mow-wt-g1-b", GoalID: "g1", ItemID: "b"},
		}, nil
	}, nil)

	m.goalWorktreesIn(t.TempDir(), false)

	if !findEntry(m, kindStatus, "worktree · mow-wt-g1-a · /tmp/mow-wt-1/wt") {
		t.Fatalf("missing first worktree line: %v", entryTexts(m))
	}
	if !findEntry(m, kindStatus, "worktree · mow-wt-g1-b · /tmp/mow-wt-2/wt") {
		t.Fatalf("missing second worktree line: %v", entryTexts(m))
	}
	if !findEntry(m, kindStatus, "worktrees · remove with /goal worktrees prune") {
		t.Fatalf("missing prune hint: %v", entryTexts(m))
	}
}

func TestGoalWorktreesListError(t *testing.T) {
	m := goalTestModel(t)
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		return nil, errors.New("not a git repo")
	}, nil)

	m.goalWorktreesIn(t.TempDir(), false)

	if !findEntry(m, kindError, "goal worktrees: not a git repo") {
		t.Fatalf("missing list error line: %v", entryTexts(m))
	}
}

func TestGoalWorktreesUsageError(t *testing.T) {
	m := goalTestModel(t)
	// Unknown sub-argument → usage hint; git boundary must not be touched.
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		t.Fatal("ListWorktrees called on usage error")
		return nil, nil
	}, nil)

	m.goalWorktrees([]string{"bogus"})

	if !findEntry(m, kindError, "usage: /goal worktrees [prune]") {
		t.Fatalf("missing usage line: %v", entryTexts(m))
	}
}

func TestGoalWorktreesNoEngine(t *testing.T) {
	m := goalTestModel(t)
	m.eng = nil
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		t.Fatal("ListWorktrees called without engine")
		return nil, nil
	}, nil)

	m.goalWorktrees(nil)

	if !findEntry(m, kindError, "goal worktrees: no engine workspace") {
		t.Fatalf("missing no-engine line: %v", entryTexts(m))
	}
}

func TestGoalWorktreesPruneNone(t *testing.T) {
	m := goalTestModel(t)
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		return nil, nil
	}, nil)

	m.goalWorktreesIn(t.TempDir(), true)

	if !findEntry(m, kindStatus, "worktrees · none") {
		t.Fatalf("prune with nothing left should say none: %v", entryTexts(m))
	}
}

func TestGoalWorktreesPruneRemovesAll(t *testing.T) {
	m := goalTestModel(t)
	type call struct{ args []string }
	var calls []call
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		return []goal.WorktreeInfo{
			{Dir: "/tmp/mow-wt-1/wt", Branch: "mow-wt-g1-a"},
			{Dir: "/tmp/mow-wt-2/wt", Branch: "mow-wt-g1-b"},
		}, nil
	}, func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, call{args: append([]string(nil), args...)})
		return nil
	})

	m.goalWorktreesIn(t.TempDir(), true)

	if !findEntry(m, kindStatus, "prune · removed 2 worktree(s)") {
		t.Fatalf("missing prune summary: %v", entryTexts(m))
	}
	// Same commands as mow's worktree cleanup(): remove, then branch -D.
	want := [][]string{
		{"worktree", "remove", "--force", "/tmp/mow-wt-1/wt"},
		{"branch", "-D", "mow-wt-g1-a"},
		{"worktree", "remove", "--force", "/tmp/mow-wt-2/wt"},
		{"branch", "-D", "mow-wt-g1-b"},
	}
	if len(calls) != len(want) {
		t.Fatalf("git calls=%v want %v", calls, want)
	}
	for i, w := range want {
		if strings.Join(calls[i].args, " ") != strings.Join(w, " ") {
			t.Fatalf("call %d = %v want %v", i, calls[i].args, w)
		}
	}
}

func TestGoalWorktreesPruneFailure(t *testing.T) {
	m := goalTestModel(t)
	stubWorktrees(t, func(context.Context, string) ([]goal.WorktreeInfo, error) {
		return []goal.WorktreeInfo{{Dir: "/tmp/mow-wt-1/wt", Branch: "mow-wt-g1-a"}}, nil
	}, func(_ context.Context, _ string, args ...string) error {
		return errors.New("git " + strings.Join(args, " ") + ": locked")
	})

	m.goalWorktreesIn(t.TempDir(), true)

	if !findEntry(m, kindError, "prune · failed mow-wt-g1-a") {
		t.Fatalf("missing prune failure line: %v", entryTexts(m))
	}
	if findEntry(m, kindStatus, "prune · removed") {
		t.Fatalf("must not report success when a prune failed: %v", entryTexts(m))
	}
}

func TestGoalWorktreeConflictDetection(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"A step escalated: merge conflict in b — worktree kept at /tmp/mow-wt-x/wt (branch mow-wt-g1-b); resolve and merge, or reject", true},
		{"A step escalated: Merge conflict in b", true},
		{"Should this change be deployed?", false},
		{"", false},
	}
	for _, c := range cases {
		if got := goalWorktreeConflict(c.in); got != c.want {
			t.Errorf("goalWorktreeConflict(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestGoalBlockedHintOnWorktreeConflict(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleGoalEvent(goal.Event{
		Kind: goal.EventBlocked,
		State: goal.State{
			ID:       "g1",
			Status:   goal.StatusBlocked,
			Question: "A step escalated: merge conflict in b — worktree kept at /tmp/mow-wt-x/wt (branch mow-wt-g1-b); resolve and merge, or reject",
		},
	})
	if !findEntry(m, kindStatus, "goal · g1 blocked — leftover worktree: /goal worktrees prune") {
		t.Fatalf("missing worktree prune hint: %v", entryTexts(m))
	}
}

func TestGoalBlockedNoHintWithoutConflict(t *testing.T) {
	m := goalTestModel(t)
	_ = m.handleGoalEvent(goal.Event{
		Kind:  goal.EventBlocked,
		State: goal.State{ID: "g1", Status: goal.StatusBlocked, Question: "Should this change be deployed?"},
	})
	if findEntry(m, kindStatus, "leftover worktree") {
		t.Fatalf("hint must not render for non-conflict blocks: %v", entryTexts(m))
	}
}

// The session token counter must count each goal's tokens exactly once:
// rerunning the SAME goal adds only the NEW tokens (State.InputTokens is
// cumulative across runs), and switching to a different goal resets the
// baseline (its cumulative starts near zero).
func TestGoalTokenCounterPerGoal(t *testing.T) {
	m := goalTestModel(t)

	// Goal A, run 1: steps report cumulative 1_000 then 2_000.
	_ = m.handleGoalEvent(goal.Event{Kind: goal.EventStep, State: goal.State{ID: "a", InputTokens: 1_000}})
	_ = m.handleGoalEvent(goal.Event{Kind: goal.EventStep, State: goal.State{ID: "a", InputTokens: 2_000}})
	if m.tokIn != 2_000 {
		t.Fatalf("goal A run1: tokIn=%d want 2000", m.tokIn)
	}

	// Goal A, run 2: State.InputTokens is cumulative across runs (monotonic),
	// so the baseline stays and only the NEW tokens count — 3500 total, NOT
	// 2000 (run1) + 3500 (rerun). The old code reset the baseline at run
	// start and double-counted every prior run.
	_ = m.handleGoalEvent(goal.Event{Kind: goal.EventStep, State: goal.State{ID: "a", InputTokens: 2_500}})
	_ = m.handleGoalEvent(goal.Event{Kind: goal.EventStep, State: goal.State{ID: "a", InputTokens: 3_500}})
	if m.tokIn != 3_500 {
		t.Fatalf("goal A rerun: tokIn=%d want 3500 (no double count)", m.tokIn)
	}

	// Different goal B: baseline must reset (its cumulative starts near zero),
	// so its tokens count once from 0.
	_ = m.handleGoalEvent(goal.Event{Kind: goal.EventStep, State: goal.State{ID: "b", InputTokens: 800}})
	if m.tokIn != 4_300 {
		t.Fatalf("goal switch: tokIn=%d want 4300", m.tokIn)
	}
}
