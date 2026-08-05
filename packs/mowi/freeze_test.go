package mowi

import (
	"fmt"
	xansi "github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// maxUpdateMs is the budget for one Update while busy streaming.
// Above this, spinner animation and input feel frozen on a typical terminal.
const maxUpdateMs = 25

func busyStreamModel(t *testing.T) *model {
	t.Helper()
	m := newModel(testEngine(t), true, false)
	m.width, m.height = 100, 40
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.busy = true
	m.startedAt = time.Now()
	m.followBottom = true
	// Seed some history so cache/rebuild costs are realistic.
	for i := 0; i < 20; i++ {
		m.add(kindUser, fmt.Sprintf("user question %d with some body text", i))
		m.add(kindAssistant, strings.Repeat("prior answer text. ", 40)+fmt.Sprintf("#%d", i))
	}
	m.refreshVP()
	return m
}

func TestFreeze_StreamSnapUpdateBudget(t *testing.T) {
	m := busyStreamModel(t)
	var max time.Duration
	var sum time.Duration
	const n = 200
	for i := 0; i < n; i++ {
		// Mix reasoning + content like DeepSeek/ZenMux.
		msg := streamSnapMsg{
			reasoning: "think step ",
			content:   fmt.Sprintf("tok%d ", i),
		}
		start := time.Now()
		mod, _ := m.Update(msg)
		d := time.Since(start)
		m = mod.(*model)
		if d > max {
			max = d
		}
		sum += d
		if d > maxUpdateMs*time.Millisecond {
			t.Fatalf("Update streamSnap took %v (budget %dms) at i=%d frameLen=%d",
				d, maxUpdateMs, i, len(m.streamFrame))
		}
	}
	avg := sum / n
	t.Logf("streamSnap Update: n=%d avg=%v max=%v streamBuf=%d reasonBuf=%d",
		n, avg, max, len(m.streamBuf), len(m.reasonBuf))
	if !strings.Contains(m.streamFrame, "tok") {
		t.Fatalf("expected progressive frame, got %q", truncate(m.streamFrame, 80))
	}
	// Must have grown (not only paint at end).
	if len(m.streamBuf) < 100 {
		t.Fatalf("streamBuf too small: %d", len(m.streamBuf))
	}
}

func TestFreeze_HeartbeatDuringStreamFlood(t *testing.T) {
	m := busyStreamModel(t)
	// Submit-style heartbeat owns spinner + elapsed on the activity band.
	const rounds = 100
	var beats int
	var lastBand string
	changed := 0
	for i := 0; i < rounds; i++ {
		start := time.Now()
		mod, _ := m.Update(streamSnapMsg{content: "x", reasoning: "r"})
		m = mod.(*model)
		if time.Since(start) > maxUpdateMs*time.Millisecond {
			t.Fatalf("stream Update %v over budget before heartbeat", time.Since(start))
		}
		// Heartbeat must stay cheap and keep rescheduling.
		m.startedAt = time.Now().Add(-time.Duration(i) * 100 * time.Millisecond)
		start = time.Now()
		mod, cmd := m.Update(busyHeartbeatMsg{})
		m = mod.(*model)
		d := time.Since(start)
		if d > maxUpdateMs*time.Millisecond {
			t.Fatalf("busyHeartbeatMsg took %v (budget %dms) — UI would freeze", d, maxUpdateMs)
		}
		if cmd == nil {
			t.Fatal("heartbeat must reschedule")
		}
		beats++
		band := xansi.Strip(m.renderActivityBand())
		if band != lastBand {
			changed++
			lastBand = band
		}
	}
	if beats < rounds {
		t.Fatalf("beats=%d", beats)
	}
	// Elapsed on the activity band should advance across heartbeats.
	if changed < 2 {
		t.Fatalf("activity band should change across heartbeats (elapsed/spinner), changed=%d last=%q", changed, lastBand)
	}
	if !strings.Contains(lastBand, "s") {
		t.Fatalf("activity band should include elapsed seconds, got %q", lastBand)
	}
}

func TestFreeze_ViewBudgetWhileStreaming(t *testing.T) {
	m := busyStreamModel(t)
	// Grow a sizable live buffer.
	for i := 0; i < 50; i++ {
		mod, _ := m.Update(streamSnapMsg{
			content:   strings.Repeat("word ", 20),
			reasoning: strings.Repeat("r ", 10),
		})
		m = mod.(*model)
	}
	const n = 50
	var max time.Duration
	for i := 0; i < n; i++ {
		start := time.Now()
		_ = m.View()
		d := time.Since(start)
		if d > max {
			max = d
		}
		if d > maxUpdateMs*time.Millisecond {
			t.Fatalf("View took %v (budget %dms) — redraw would freeze spinner", d, maxUpdateMs)
		}
	}
	t.Logf("View while streaming: max=%v", max)
}

func TestFreeze_ConcurrentIngestPollUpdate(t *testing.T) {
	// Mimics production: LLM goroutine pushes tokens; UI drains via pollStream + Update.
	m := busyStreamModel(t)
	ing := newStreamIngest()
	m.ingest = ing

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			ing.pushReasoning("think ")
			ing.pushContent(fmt.Sprintf("c%d ", i))
			// Simulate fast SSE without sleeping (worst case for UI).
		}
		ing.finish()
	}()

	// Drain like Bubble Tea would: pollStream cmd → Update(snap) → re-poll.
	deadline := time.Now().Add(3 * time.Second)
	var snaps int
	var maxUpd time.Duration
	for {
		if time.Now().After(deadline) {
			t.Fatal("timeout draining ingest — possible deadlock/freeze")
		}
		cmd := m.pollStream()
		if cmd == nil {
			t.Fatal("pollStream nil")
		}
		// Don't block forever if finish raced empty.
		type result struct {
			msg tea.Msg
		}
		ch := make(chan result, 1)
		go func() { ch <- result{msg: cmd()} }()
		var msg tea.Msg
		select {
		case r := <-ch:
			msg = r.msg
		case <-time.After(500 * time.Millisecond):
			// Writer may still be going; retry.
			select {
			case <-done:
				// finished but no wake — take leftover
				c, r, fin := ing.take()
				if c == "" && r == "" && fin {
					goto drained
				}
				msg = streamSnapMsg{content: c, reasoning: r, finished: fin}
			default:
				continue
			}
		}
		snap, ok := msg.(streamSnapMsg)
		if !ok {
			t.Fatalf("unexpected msg %T", msg)
		}
		start := time.Now()
		mod, next := m.Update(snap)
		d := time.Since(start)
		m = mod.(*model)
		if d > maxUpd {
			maxUpd = d
		}
		if d > maxUpdateMs*time.Millisecond {
			t.Fatalf("Update after concurrent ingest took %v at snap=%d", d, snaps)
		}
		snaps++
		// Exercise heartbeat between snaps.
		mod, _ = m.Update(busyHeartbeatMsg{})
		m = mod.(*model)
		_ = next
		if snap.finished {
			break
		}
	}
drained:
	<-done
	t.Logf("concurrent drain: snaps=%d maxUpdate=%v buf=%d reason=%d",
		snaps, maxUpd, len(m.streamBuf), len(m.reasonBuf))
	if len(m.streamBuf) == 0 {
		t.Fatal("no content drained — stream would appear empty then dump on done")
	}
	if !strings.Contains(m.streamFrame, "c") {
		t.Fatalf("live frame missing content: %q", truncate(m.streamFrame, 100))
	}
}

func TestFreeze_DoneGlamourOffUpdatePath(t *testing.T) {
	// doneMsg must not sync-glamour a large answer on Update.
	m := busyStreamModel(t)
	m.streamBuf = strings.Repeat("# Title\n\n```go\nfunc main() {}\n```\n\n", 30)
	final := m.streamBuf
	start := time.Now()
	mod, cmd := m.Update(doneMsg{text: final})
	d := time.Since(start)
	m = mod.(*model)
	if d > maxUpdateMs*time.Millisecond {
		t.Fatalf("doneMsg Update took %v — likely sync glamour on Update path", d)
	}
	if m.busy {
		t.Fatal("should not be busy")
	}
	// Pretty work is a tea.Cmd (async), not inline.
	if cmd == nil {
		t.Fatal("expected cmds including async pretty")
	}
	t.Logf("doneMsg Update=%v", d)
}

func TestFreeze_AsyncGlamourCost(t *testing.T) {
	// Pretty markdown after done is async — still measure cost so we know
	// if entryPrettyMsg freezes the UI when applied.
	pinTerminalTheme()
	c := newMDCache(true)
	md := strings.Repeat("## Heading\n\nSome **bold** and `code`.\n\n```go\nfunc main(){}\n```\n\n", 25)
	start := time.Now()
	out := renderMarkdownCached(&c, md, 80, false)
	d := time.Since(start)
	t.Logf("async glamour (typical answer size) dur=%v out_len=%d", d, len(out))
	// Soft budget: if glamour takes >200ms, entryPretty apply will hitch after stream.
	if d > 500*time.Millisecond {
		t.Fatalf("glamour too slow: %v — will feel like freeze when pretty lands", d)
	}
}

func TestFreeze_PaintLiveStreamIsolated(t *testing.T) {
	m := busyStreamModel(t)
	m.streamBuf = strings.Repeat("token ", 400)
	m.reasonBuf = strings.Repeat("think ", 200)
	const n = 100
	var max time.Duration
	for i := 0; i < n; i++ {
		m.streamBuf += "x"
		start := time.Now()
		m.paintLiveStream()
		d := time.Since(start)
		if d > max {
			max = d
		}
		if d > maxUpdateMs*time.Millisecond {
			t.Fatalf("paintLiveStream %v over budget", d)
		}
	}
	t.Logf("paintLiveStream max=%v", max)
}
