package mowi

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestAppendPeerDeltaKeepsFullCommitAndUTF8Tail(t *testing.T) {
	m := freshModel(t)
	first := "intro — keep this markdown\n\n```go\n"
	m.appendPeerDelta("opus", first)
	m.appendPeerDelta("opus", strings.Repeat("x", maxPeerBufBytes)+"\n```\n")
	b := m.peerBufs[peerKey("opus")]
	if b == nil {
		t.Fatal("peer buffer missing")
	}
	if !strings.HasPrefix(b.full, first) {
		t.Fatalf("full peer answer lost its prefix: %q", b.full[:min(len(b.full), 80)])
	}
	if len(b.buf) > maxPeerBufBytes {
		t.Fatalf("display buffer exceeded cap: %d", len(b.buf))
	}
	if !utf8.ValidString(b.buf) {
		t.Fatal("display tail is invalid UTF-8")
	}
	if strings.Contains(b.full, "intro — keep this markdown") == false {
		t.Fatal("full answer missing original text")
	}
}

func TestTrimUTF8Tail(t *testing.T) {
	in := strings.Repeat("x", maxPeerBufBytes-1) + "🙂"
	got := trimUTF8Tail(in, maxPeerBufBytes)
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed tail is invalid UTF-8: %q", got)
	}
}

func TestPeerDeltaIngestPreservesChunksAndAgentOrder(t *testing.T) {
	p := newPeerDeltaIngest()
	p.push("opus", "## O")
	p.push("opus", "pus\n")
	p.push("grok", "hello")
	got := p.take()
	if len(got) != 2 {
		t.Fatalf("got %d peer batches, want 2: %#v", len(got), got)
	}
	if got[0].agent != "opus" || got[0].text != "## Opus\n" {
		t.Fatalf("opus chunks were not preserved: %#v", got[0])
	}
	if got[1].agent != "grok" || got[1].text != "hello" {
		t.Fatalf("grok chunk was not preserved: %#v", got[1])
	}
	if rest := p.take(); len(rest) != 0 {
		t.Fatalf("take should drain the ingest: %#v", rest)
	}
}

func TestPeerCommitUsesFullReplyAfterDisplayTrim(t *testing.T) {
	m := goalTestModel(t)
	m.busy = true
	prefix := "# Opus reply\n\n"
	m.appendPeerDelta("opus", prefix)
	m.appendPeerDelta("opus", strings.Repeat("z", maxPeerBufBytes)+"\n")
	before := len(m.entries)
	cmd := m.finishPeerStream("opus")
	if len(m.entries) != before+2 {
		t.Fatalf("entries grew by %d, want status + reply", len(m.entries)-before)
	}
	got := m.entries[len(m.entries)-1].text
	if got != strings.TrimRight(prefix+strings.Repeat("z", maxPeerBufBytes)+"\n", " \t\r\n") {
		t.Fatalf("peer commit lost full reply: got %d bytes, want %d", len(got), len(prefix)+maxPeerBufBytes)
	}
	if cmd == nil {
		t.Fatal("long peer reply should schedule markdown rendering")
	}
}

// TestPeerLabelAlignment: short and long peer names must share one body
// column — labels are right-padded to the widest "→ name" so blocks align.
func TestPeerLabelAlignment(t *testing.T) {
	m := freshModel(t)
	short, long := "peer-a", "claude-sonnet-4"
	w := max(peerLabelWidth(short), peerLabelWidth(long))
	if peerLabelWidth(long) <= peerLabelWidth(short) {
		t.Fatalf("long name should be wider: %d vs %d", peerLabelWidth(long), peerLabelWidth(short))
	}
	l1 := m.peerLabel(short, w)
	l2 := m.peerLabel(long, w)
	if got, want := xansi.StringWidth(l1), xansi.StringWidth(l2); got != want {
		t.Fatalf("labels not equal width: %q=%d vs %q=%d", l1, got, l2, want)
	}
	if !strings.Contains(l1, glyphArrow) || !strings.Contains(l2, glyphArrow) {
		t.Fatalf("peer labels must carry the arrow glyph: %q / %q", l1, l2)
	}
}

// TestPeerSepRule: a faint rule separates two peers' blocks; a lone peer has
// no separator (nothing to bound).
func TestPeerSepRule(t *testing.T) {
	m := freshModel(t)
	// Structure below is the expanded (live text) layout; collapsed mode
	// paints one summary line per peer instead.
	m.peerExpanded = true
	m.appendPeerDelta("peer-a", "first answer")
	m.appendPeerDelta("peer-b", "second answer")
	out := m.peerLiveBodies(40)
	if !strings.Contains(out, "\u2500") {
		t.Fatalf("expected a separator rule between two peers:\n%s", out)
	}
	// Rule sits between the two bodies, not at the very start or end.
	if strings.HasPrefix(strings.TrimSpace(out), "\u2500") || strings.HasSuffix(strings.TrimSpace(out), "\u2500") {
		t.Fatalf("rule should be interior, not a leading/trailing line:\n%s", out)
	}

	m.clearPeerBufs()
	m.appendPeerDelta("peer-a", "only answer")
	single := m.peerLiveBodies(40)
	if strings.Contains(single, "\u2500") {
		t.Fatalf("single peer must not draw a separator:\n%s", single)
	}
}

// TestPeerEmptyShowsCaret: a registered peer with no tokens yet still renders
// its label + caret so all in-flight peers stay visible while pending.
func TestPeerEmptyShowsCaret(t *testing.T) {
	m := freshModel(t)
	// Structure below is the expanded (live text) layout; collapsed mode
	// paints one summary line per peer instead.
	m.peerExpanded = true
	m.ensurePeerBuf("peer-c") // no delta appended
	out := m.peerLiveBodies(40)
	if out == "" {
		t.Fatal("pending peer with empty buffer should still render")
	}
	if !strings.Contains(out, "peer-c") {
		t.Fatalf("pending peer label missing:\n%s", out)
	}
	if !strings.Contains(out, glyphCaret) {
		t.Fatalf("pending peer should show a caret placeholder:\n%s", out)
	}
}

// TestPeerLabelIndentedUnderGutter: the "→ agent" tag must start in the same
// column as its body — both are indented by the role gutter, so the label does
// not hang flush-left off its own indented block.
func TestPeerLabelIndentedUnderGutter(t *testing.T) {
	m := freshModel(t)
	// Structure below is the expanded (live text) layout; collapsed mode
	// paints one summary line per peer instead.
	m.peerExpanded = true
	m.appendPeerDelta("peer-a", "answer text")
	out := m.peerLiveBodies(40)
	lines := strings.Split(out, "\n")
	// Find the label line (carries the arrow) and a body line (answer text).
	var labelLine, bodyLine string
	for _, ln := range lines {
		if strings.Contains(ln, glyphArrow) {
			labelLine = ln
		}
		if strings.Contains(ln, "answer text") {
			bodyLine = ln
		}
	}
	if labelLine == "" || bodyLine == "" {
		t.Fatalf("missing label/body lines:\n%s", out)
	}
	lead := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	if lead(labelLine) != lead(bodyLine) {
		t.Fatalf("label indent %d != body indent %d:\n%q\n%q", lead(labelLine), lead(bodyLine), labelLine, bodyLine)
	}
	if lead(labelLine) != roleGutterW {
		t.Fatalf("label should sit in the role gutter (%d), got %d: %q", roleGutterW, lead(labelLine), labelLine)
	}
}

// TestPeerSepRuleSpansBody: the separator rule between two peers spans the peer
// body width (not a fixed short cap) so it bounds the block it separates.
func TestPeerSepRuleSpansBody(t *testing.T) {
	m := freshModel(t)
	// Structure below is the expanded (live text) layout; collapsed mode
	// paints one summary line per peer instead.
	m.peerExpanded = true
	m.appendPeerDelta("peer-a", "first")
	m.appendPeerDelta("peer-b", "second")
	const inner = 60
	out := m.peerLiveBodies(inner)
	var ruleLen int
	for _, ln := range strings.Split(out, "\n") {
		if c := strings.Count(ln, "\u2500"); c > ruleLen {
			ruleLen = c
		}
	}
	if ruleLen != inner {
		t.Fatalf("rule should span the %d-col body, got %d dashes:\n%s", inner, ruleLen, out)
	}
}

// Peer progress stays low-priority: caret, separator rule, and the plain
// fallback body must render faint so an in-flight acp_delegate answer never
// competes with the host's main content. The label NAME is the exception —
// accent+bold (see TestPeerLiveLabelAccent) — the dimming lives on the body.
func TestPeerLiveFaint(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	// Body text only exists when peers are expanded; collapsed mode paints a
	// one-line summary (covered by TestPeerLiveCollapsedByDefault).
	m.peerExpanded = true
	m.appendPeerDelta("peer-a", "progress note")
	body := m.peerLiveBodies(60)
	// lipgloss merges faint+color into one SGR ("2;38;2;…") — look for the
	// dim attribute, not a bare ESC[2m.
	if !strings.Contains(body, "\x1b[2;") && !strings.Contains(body, ";2;") {
		t.Fatalf("peer live progress not faint: %q", body)
	}
	// The faint span must cover the body text, not only the arrow glyph.
	i := strings.Index(body, "progress note")
	if i < 0 {
		t.Fatalf("body text missing: %q", body)
	}
	// Find the last SGR opener before the body text; it must carry the faint
	// attribute (2) so the body itself is dim.
	open := strings.LastIndex(body[:i], "\x1b[")
	if open < 0 || !strings.Contains(body[open:i], ";2;") && !strings.HasPrefix(body[open:open+6], "\x1b[2;") {
		t.Fatalf("body text not under a faint span: %q", body)
	}
}

// The label name is the one accent element on a peer line: the user must see
// WHO is speaking at a glance while the body stays dim. The arrow stays faint
// chrome — accent is spent on the name only.
func TestPeerLiveLabelAccent(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.appendPeerDelta("peer-a", "x")
	body := m.peerLiveBodies(60)
	// Assert against the style the model actually renders with: freshModel's
	// theme accent (the raw defaultPalette value belongs to another profile).
	want := m.theme.Accent.Bold(true).Render("peer-a")
	if !strings.Contains(body, want) {
		t.Fatalf("peer name not rendered accent+bold (want span %q):\n%s", want, body)
	}
	ni := strings.Index(body, "peer-a")
	if ni < 0 {
		t.Fatalf("peer name missing: %q", body)
	}
	span := body[strings.LastIndex(body[:ni], "\x1b["):ni]
	if !strings.Contains(span, "38;2;") {
		t.Fatalf("peer name span carries no color: %q", span)
	}
	if !strings.Contains(span, ";1;") && !strings.HasPrefix(span, "\x1b[1;") {
		t.Fatalf("peer name not bold: %q", span)
	}
	// Arrow before the name must NOT share the name's colored span (it stays
	// faint chrome — accent is spent on the name only).
	ai := strings.Index(body, glyphArrow)
	if ai < 0 {
		t.Fatalf("arrow missing: %q", body)
	}
	arrowSpan := body[strings.LastIndex(body[:ai], "\x1b["):ai]
	if !strings.Contains(arrowSpan, ";2;") && !strings.HasPrefix(arrowSpan, "\x1b[2;") {
		t.Fatalf("arrow should stay faint chrome: %q", arrowSpan)
	}
}

// The live peer buffer is capped so a long reasoning stream cannot grow it
// without bound; the tail is kept and the cached body is invalidated.
func TestAppendPeerDeltaCapsBuffer(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	chunk := strings.Repeat("x", 1024)
	for i := 0; i < 20; i++ {
		m.appendPeerDelta("peer-agent", chunk)
	}
	b := m.peerBufs[peerKey("peer-agent")]
	if b == nil {
		t.Fatal("peer buffer missing")
	}
	if len(b.buf) > maxPeerBufBytes {
		t.Fatalf("peer buffer exceeded cap: %d > %d", len(b.buf), maxPeerBufBytes)
	}
	if b.body != "" || b.bodySrc != "" {
		t.Fatalf("cached body should be invalidated on trim: %q", b.body)
	}
}

// Regression: a native mow agent's reply must commit markdown-complete and
// pretty-eligible. Reproduces the reported corruption shape — the reply text
// arriving duplicated (chunked copy + full result copy, exactly what the mow
// acp peer emitted before EventDelegateChunk stopped being forwarded as
// agent_message_chunk) — plus the normal token-stream path.
func TestPeerCommitMarkdownNativeReply(t *testing.T) {
	const reply = "## Summary\n\n- one\n- two\n\n```go\nfmt.Println(1)\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"

	t.Run("token stream commits complete and pretty-eligible", func(t *testing.T) {
		m := goalTestModel(t)
		base := len(m.entries)
		// Small token-ish chunks, splitting mid-fence and mid-table.
		for i := 0; i < len(reply); i += 7 {
			end := min(i+7, len(reply))
			m.appendPeerDelta("gemini", reply[i:end])
		}
		cmd := m.finishPeerStream("gemini")
		if len(m.entries) != base+2 { // status line + assistant entry
			t.Fatalf("entries grew by %d, want 2", len(m.entries)-base)
		}
		got := m.entries[len(m.entries)-1]
		if got.kind != kindAssistant {
			t.Fatalf("last entry kind = %v, want assistant", got.kind)
		}
		if got.text != strings.TrimRight(reply, " \t\r\n") {
			t.Fatalf("committed text corrupted:\n%q\nwant:\n%q", got.text, reply)
		}
		if cmd == nil {
			t.Fatal("finishPeerStream returned no pretty cmd for markdown reply")
		}
		if len(m.peerBufs) != 0 {
			t.Fatalf("peer buf not cleared: %v", m.peerBufs)
		}
		// The async pretty render must produce markdown formatting (ANSI),
		// not the plain/faint live body.
		msg := cmd()
		pm, ok := msg.(entryPrettyMsg)
		if !ok {
			t.Fatalf("pretty cmd msg = %T, want entryPrettyMsg", msg)
		}
		if !strings.Contains(pm.body, "\x1b[") {
			t.Fatalf("pretty body is plain — markdown not rendered:\n%q", pm.body)
		}
	})

	t.Run("mow acp peer wire keeps delegate chunks out of reply text", func(t *testing.T) {
		// Source-level regression for the actual bug: agent.go must not forward
		// EventDelegateChunk via writeAgentText (that duplicated nested peer
		// answers into the reply the host commits, breaking markdown).
		src, err := os.ReadFile("../mow/ext/acp/agent.go")
		if err != nil {
			t.Skipf("mow source not available: %v", err)
		}
		body := string(src)
		idx := strings.Index(body, "case mow.EventDelegateChunk:")
		if idx < 0 {
			t.Skip("agent.go has no EventDelegateChunk case")
		}
		// The case body ends at the next case clause.
		seg := body[idx:]
		if end := strings.Index(seg, "\n\t\t\tcase mow."); end > 0 {
			seg = seg[:end]
		}
		if strings.Contains(seg, "writeAgentText(") {
			t.Fatalf("EventDelegateChunk forwarded as agent_message_chunk (reply-text pollution):\n%s", seg)
		}
	})
}

// Peer streams collapse to a single status line by default. Painting a
// delegate's full reasoning live is what produced the flicker (a multi-line
// region repainted on every chunk) and made text selection impossible (the
// selected region kept changing underneath the mouse).
func TestPeerLiveCollapsedByDefault(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	if !m.peerLiveCollapsed() {
		t.Fatal("peers must start collapsed")
	}
	m.appendPeerDelta("peer-a", "line one\nline two\nline three\n")

	body := m.peerLiveBodies(60)
	if strings.Contains(body, "line two") {
		t.Fatalf("collapsed peer leaked body text: %q", body)
	}
	if n := strings.Count(body, "\n"); n != 0 {
		t.Fatalf("collapsed peer must be one line, got %d newlines: %q", n, body)
	}
	if !strings.Contains(body, "peer-a") {
		t.Fatalf("collapsed peer must still name the agent: %q", body)
	}

	// Expanded shows the text again.
	m.peerExpanded = true
	if got := m.peerLiveBodies(60); !strings.Contains(got, "line two") {
		t.Fatalf("expanded peer missing body: %q", got)
	}
}

// The collapsed summary must stay one line per peer no matter how much text
// streams in — that bounded height is what stops the transcript from jumping.
func TestPeerCollapsedHeightStableAsTextGrows(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.appendPeerDelta("peer-a", "first")
	first := strings.Count(m.peerLiveBodies(60), "\n")

	for i := 0; i < 200; i++ {
		m.appendPeerDelta("peer-a", "more streamed reasoning text\n")
	}
	if got := strings.Count(m.peerLiveBodies(60), "\n"); got != first {
		t.Fatalf("collapsed peer height grew: %d -> %d newlines", first, got)
	}
}

// Two peers stay two lines (not two blocks).
func TestPeerCollapsedOneLinePerPeer(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.appendPeerDelta("peer-a", "aaa\nbbb\n")
	m.appendPeerDelta("peer-b", "ccc\nddd\n")
	body := m.peerLiveBodies(60)
	if n := strings.Count(body, "\n"); n != 1 {
		t.Fatalf("two collapsed peers want 1 newline, got %d: %q", n, body)
	}
}

func TestHumanCount(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0 chars"},
		{999, "999 chars"},
		{1500, "1.5k chars"},
		{2_500_000, "2.5M chars"},
	} {
		if got := humanCount(tc.in); got != tc.want {
			t.Errorf("humanCount(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
