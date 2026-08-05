package mowi

import (
	"context"
	"fmt"
	xansi "github.com/charmbracelet/x/ansi"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/subosito/mow"
)

// progressiveStream simulates token arrival without importing mow/internal/llm.
// onContent is called for each token; returns the full content string.
func progressiveStream(tokens []string, interval, initialDelay time.Duration, onContent func(string)) string {
	if initialDelay > 0 {
		time.Sleep(initialDelay)
	}
	var b strings.Builder
	for i, tok := range tokens {
		if onContent != nil {
			onContent(tok)
		}
		b.WriteString(tok)
		if interval > 0 && i < len(tokens)-1 {
			time.Sleep(interval)
		}
	}
	return b.String()
}

func TestE2E_ProgressiveStreamHelper(t *testing.T) {
	tokens := []string{"Hel", "lo", " ", "wor", "ld", "!"}
	var got []string
	var at []time.Time
	start := time.Now()
	content := progressiveStream(tokens, 30*time.Millisecond, 0, func(d string) {
		got = append(got, d)
		at = append(at, time.Now())
	})
	if content != "Hello world!" {
		t.Fatalf("content=%q", content)
	}
	if len(got) != len(tokens) {
		t.Fatalf("got %d tokens want %d: %v", len(got), len(tokens), got)
	}
	if len(at) >= 2 {
		span := at[len(at)-1].Sub(at[0])
		minSpan := 30 * time.Millisecond * time.Duration(len(tokens)-2)
		if span < minSpan/2 {
			t.Fatalf("tokens arrived too bunched (%v), expected progressive ~%v+", span, minSpan)
		}
		t.Logf("token span=%v from start=%v", span, at[0].Sub(start))
	}
}

func TestE2E_EngineOnTokenToTUIProgressive(t *testing.T) {
	// Full path: progressive tokens → streamIngest → pollStream → Update.
	tokens := make([]string, 40)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("t%d ", i)
	}

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "unused"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(eng, true, false)
	m.width, m.height = 100, 40
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.busy = true
	m.startedAt = time.Now()
	ing := newStreamIngest()
	m.ingest = ing

	eng.SetOnToken(ing.pushContent)
	eng.SetOnReasoning(ing.pushReasoning)

	var promptErr error
	var final string
	var promptDone atomic.Bool
	go func() {
		final = progressiveStream(tokens, 15*time.Millisecond, 0, ing.pushContent)
		ing.finish()
		promptDone.Store(true)
	}()

	deadline := time.Now().Add(5 * time.Second)
	var (
		snaps      int
		frameSizes []int
		maxUpd     time.Duration
		spinnerN   int
	)
	for !promptDone.Load() || m.streamBuf != final {
		if time.Now().After(deadline) {
			t.Fatalf("timeout snaps=%d buf=%q final=%q err=%v", snaps, m.streamBuf, final, promptErr)
		}
		start := time.Now()
		mod, scmd := m.Update(busyHeartbeatMsg{})
		m = mod.(*model)
		if time.Since(start) > maxUpdateMs*time.Millisecond {
			t.Fatalf("heartbeat froze: %v", time.Since(start))
		}
		if scmd != nil {
			spinnerN++
		}

		select {
		case <-ing.sig:
			c, r, fin := ing.take()
			if c == "" && r == "" && !fin {
				continue
			}
			start = time.Now()
			mod, _ = m.Update(streamSnapMsg{content: c, reasoning: r, finished: fin})
			d := time.Since(start)
			m = mod.(*model)
			if d > maxUpd {
				maxUpd = d
			}
			if d > maxUpdateMs*time.Millisecond {
				t.Fatalf("stream Update froze: %v", d)
			}
			snaps++
			frameSizes = append(frameSizes, len(m.streamFrame))
			if fin && promptDone.Load() {
				break
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
		if promptDone.Load() && m.streamBuf == final {
			select {
			case <-ing.sig:
				c, r, fin := ing.take()
				mod, _ = m.Update(streamSnapMsg{content: c, reasoning: r, finished: fin})
				m = mod.(*model)
				snaps++
			default:
			}
			break
		}
	}

	if snaps < 5 {
		t.Fatalf("too few progressive snaps (%d) — UI would show answer in one dump", snaps)
	}
	grew := false
	for i := 1; i < len(frameSizes); i++ {
		if frameSizes[i] > frameSizes[0] {
			grew = true
			break
		}
	}
	if !grew && len(frameSizes) > 1 {
		t.Fatalf("frame size never grew: %v", frameSizes[:min(10, len(frameSizes))])
	}
	if !strings.Contains(m.streamFrame, "t0") && !strings.Contains(m.streamBuf, "t0") {
		t.Fatalf("missing early token in buf/frame buf=%q", m.streamBuf)
	}
	t.Logf("e2e progressive: snaps=%d spinnerTicks=%d maxUpd=%v finalLen=%d",
		snaps, spinnerN, maxUpd, len(final))
}

func TestE2E_SpinnerAliveDuringLongTTFT(t *testing.T) {
	// Simulate ~400ms TTFT with no tokens; activity band must keep elapsed alive.
	tokens := []string{"ok"}
	eng, err := mow.New(mow.Options{NoSession: true, Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "x"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(eng, true, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	m.busy = true
	m.startedAt = time.Now()
	ing := newStreamIngest()
	m.ingest = ing

	go func() {
		_ = progressiveStream(tokens, 0, 400*time.Millisecond, ing.pushContent)
		ing.finish()
	}()

	deadline := time.Now().Add(350 * time.Millisecond)
	var beats int
	var maxTick time.Duration
	var bands []string
	for time.Now().Before(deadline) {
		m.startedAt = time.Now().Add(-time.Duration(beats) * 100 * time.Millisecond)
		start := time.Now()
		mod, cmd := m.Update(busyHeartbeatMsg{})
		d := time.Since(start)
		m = mod.(*model)
		if d > maxTick {
			maxTick = d
		}
		if d > maxUpdateMs*time.Millisecond {
			t.Fatalf("heartbeat froze during TTFT: %v", d)
		}
		if cmd == nil {
			t.Fatal("heartbeat must reschedule during TTFT")
		}
		bands = append(bands, xansi.Strip(m.renderActivityBand()))
		beats++
		time.Sleep(20 * time.Millisecond)
	}
	if beats < 3 {
		t.Fatalf("too few heartbeats: %d", beats)
	}
	foundElapsed := false
	for _, b := range bands {
		if strings.Contains(b, "s") {
			foundElapsed = true
			break
		}
	}
	if !foundElapsed {
		t.Fatalf("expected elapsed in activity band, samples=%v", bands)
	}
	_ = maxTick
}

func TestE2E_SubmitPathMessagesProgressive(t *testing.T) {
	tokens := []string{}
	for i := 0; i < 25; i++ {
		tokens = append(tokens, fmt.Sprintf("w%d ", i))
	}

	eng, err := mow.New(mow.Options{NoSession: true, Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "unused"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(eng, true, false)
	m.width, m.height = 80, 30
	m.layout()
	m.ready = true
	m.showWelcome = false

	m.busy = true
	m.startedAt = time.Now()
	ing := newStreamIngest()
	m.ingest = ing
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type done struct {
		text string
		err  error
	}
	doneCh := make(chan done, 1)
	go func() {
		_ = ctx
		text := progressiveStream(tokens, 20*time.Millisecond, 0, ing.pushContent)
		ing.finish()
		doneCh <- done{text: text, err: nil}
	}()

	var teaMsgs []tea.Msg
	pending := []tea.Cmd{m.pollStream(), m.scheduleBusyHeartbeat()}

	runCmd := func(c tea.Cmd) {
		if c == nil {
			return
		}
		msgCh := make(chan tea.Msg, 1)
		go func() { msgCh <- c() }()
		select {
		case msg := <-msgCh:
			teaMsgs = append(teaMsgs, msg)
		case <-time.After(50 * time.Millisecond):
			pending = append(pending, c)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	var gotDone bool
	var progressiveFrames int
	var lastFrame string
	for time.Now().Before(deadline) && !gotDone {
		old := pending
		pending = nil
		for _, c := range old {
			runCmd(c)
		}
		if m.busy && m.ingest != nil {
			runCmd(m.pollStream())
		}

		for len(teaMsgs) > 0 {
			msg := teaMsgs[0]
			teaMsgs = teaMsgs[1:]
			start := time.Now()
			mod, cmd := m.Update(msg)
			if time.Since(start) > maxUpdateMs*time.Millisecond {
				t.Fatalf("Update(%T) took %v", msg, time.Since(start))
			}
			m = mod.(*model)
			if cmd != nil {
				pending = append(pending, cmd)
			}
			if m.streamFrame != "" && m.streamFrame != lastFrame {
				progressiveFrames++
				lastFrame = m.streamFrame
			}
		}

		select {
		case d := <-doneCh:
			if d.err != nil {
				t.Fatal(d.err)
			}
			mod, cmd := m.Update(doneMsg{text: d.text})
			m = mod.(*model)
			_ = cmd
			gotDone = true
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !gotDone {
		t.Fatal("never finished")
	}
	if progressiveFrames < 3 {
		t.Fatalf("only %d distinct live frames — not progressive (freeze/dump)", progressiveFrames)
	}
	t.Logf("submit-path progressive frames=%d final entries=%v", progressiveFrames, m.lines())
}
