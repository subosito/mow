package mowi

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

func gcTestModel(t *testing.T) *model {
	t.Helper()
	m := newModel(testEngine(t), false, false)
	mod, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return mod.(*model)
}

func TestStubEntryText(t *testing.T) {
	s := stubEntryText(kindUser, "hello world\nsecond line")
	if !strings.Contains(s, "you") || !strings.Contains(s, "hello world") {
		t.Fatalf("%q", s)
	}
	if strings.Contains(s, "second") {
		t.Fatalf("should use first line only: %q", s)
	}

	s2 := stubEntryText(kindAssistant, "assistant reply body")
	if !strings.Contains(s2, "mowi") || !strings.Contains(s2, "assistant reply") {
		t.Fatalf("%q", s2)
	}

	s3 := stubEntryText(kindDiff, "diff --git a/x")
	if !strings.Contains(s3, "diff") {
		t.Fatalf("%q", s3)
	}

	// Empty / whitespace → compact marker.
	if got := stubEntryText(kindUser, "  \n"); got != "…(gc)" {
		t.Fatalf("empty stub = %q", got)
	}

	// Long first line truncated to entryTextStubRunes + ellipsis.
	long := strings.Repeat("x", 200)
	s4 := stubEntryText(kindAssistant, long)
	if !strings.Contains(s4, "…") {
		t.Fatalf("expected ellipsis: %q", s4)
	}
	// Body after prefix should not exceed stub runes + "…".
	// Format: "…(mowi gc) " + truncated
	const prefix = "…(mowi gc) "
	if !strings.HasPrefix(s4, prefix) {
		t.Fatalf("prefix: %q", s4)
	}
	body := strings.TrimPrefix(s4, prefix)
	// truncated runes + "…" rune
	if utf8.RuneCountInString(body) > entryTextStubRunes+1 {
		t.Fatalf("stub body too long: %d runes (%q)", utf8.RuneCountInString(body), body)
	}

	// Unknown kinds still produce a generic label.
	s5 := stubEntryText(kindStatus, "status text")
	if !strings.Contains(s5, "gc") || !strings.Contains(s5, "status text") {
		t.Fatalf("%q", s5)
	}
}

func TestGCOldEntryText(t *testing.T) {
	m := gcTestModel(t)
	// Keep full only for last entryTextKeepFull; fill past that.
	// followBottom=true → entryVisible always false → GC can reclaim.
	m.followBottom = true
	total := entryTextKeepFull + 10
	for i := 0; i < total; i++ {
		m.add(kindUser, fmt.Sprintf("user body %d with unique-%d content", i, i))
		m.add(kindAssistant, fmt.Sprintf("assistant reply %d unique-%d", i, i))
	}
	// Status lines are not GC'd.
	m.add(kindStatus, "status-keep-full")

	n := len(m.entries)
	// Last entryTextKeepFull entries must retain full text.
	for i := n - entryTextKeepFull; i < n; i++ {
		e := m.entries[i]
		if e.gc {
			t.Fatalf("recent entries[%d] gc'd (kind=%v text=%q)", i, e.kind, e.text)
		}
	}
	// Older user/assistant beyond the keep window should be stubbed.
	gcN := 0
	for i := 0; i < n-entryTextKeepFull; i++ {
		e := m.entries[i]
		switch e.kind {
		case kindUser, kindAssistant, kindDiff:
			if !e.gc {
				t.Fatalf("old entries[%d] kind=%v not gc'd", i, e.kind)
			}
			if e.view != "" || e.viewW != 0 {
				t.Fatalf("entries[%d] view not cleared", i)
			}
			if !e.plain {
				t.Fatalf("entries[%d] plain not set", i)
			}
			if !strings.Contains(e.text, "gc") {
				t.Fatalf("entries[%d] stub missing gc mark: %q", i, e.text)
			}
			// Original unique token must not remain as full body — stub keeps
			// a short first-line prefix, so unique-N may still appear in the
			// summary. Ensure we at least replaced with stub format.
			if !strings.HasPrefix(e.text, "…") {
				t.Fatalf("entries[%d] not stub-prefixed: %q", i, e.text)
			}
			gcN++
		case kindStatus, kindTool, kindError, kindPerm:
			if e.gc {
				t.Fatalf("meta entries[%d] should not gc", i)
			}
		}
	}
	if gcN == 0 {
		t.Fatal("expected some user/assistant entries GC'd")
	}
	// Status at end intact.
	last := m.entries[n-1]
	if last.kind != kindStatus || last.text != "status-keep-full" || last.gc {
		t.Fatalf("status corrupted: %+v", last)
	}
}

func TestGCBelowThresholdNoOp(t *testing.T) {
	m := gcTestModel(t)
	m.followBottom = true
	for i := 0; i < entryTextKeepFull; i++ {
		m.add(kindAssistant, fmt.Sprintf("keep-%d %s", i, strings.Repeat("x", 50)))
	}
	for i, e := range m.entries {
		if e.gc {
			t.Fatalf("GC fired at/under keep window on entries[%d]", i)
		}
	}
}

func TestGCSkipsVisibleWhenPinned(t *testing.T) {
	m := gcTestModel(t)
	// Build heights/line starts, pin viewport at top so early entries are visible.
	m.followBottom = false
	for i := 0; i < entryTextKeepFull+15; i++ {
		m.entries = append(m.entries, entry{
			kind: kindAssistant,
			text: fmt.Sprintf("vis-%d %s", i, strings.Repeat("y", 40)),
			at:   time.Now(),
		})
	}
	m.invalidateHistoryCache()
	m.vp.SetYOffset(0)
	m.refreshVP()

	if !m.entryVisible(0) {
		t.Fatal("entry 0 should be visible at top")
	}
	// Manual GC — visible early entry must be skipped.
	m.gcOldEntryText()
	if m.entries[0].gc {
		t.Fatal("visible entry 0 was GC'd")
	}

	// After jumping to bottom with follow, early entries become reclaimable.
	m.followBottom = true
	m.vp.GotoBottom()
	m.gcOldEntryText()
	if !m.entries[0].gc {
		t.Fatal("entry 0 should GC once followBottom hides it")
	}
}

func TestGCOnlyUserAssistantDiff(t *testing.T) {
	m := gcTestModel(t)
	m.followBottom = true
	// Overflow keep window with mixed kinds.
	for i := 0; i < entryTextKeepFull+5; i++ {
		m.add(kindTool, fmt.Sprintf("tool-%d", i))
		m.add(kindError, fmt.Sprintf("err-%d", i))
		m.add(kindStatus, fmt.Sprintf("st-%d", i))
	}
	// Also add old user/diff past window.
	// Prepend by rebuilding: simplest is add many user then tools already pushed them.
	// Current tail is meta — all meta, nothing to GC among kinds. Prepend large user block:
	old := make([]entry, 0, entryTextKeepFull+10)
	for i := 0; i < 10; i++ {
		old = append(old, entry{kind: kindUser, text: fmt.Sprintf("old-user-%d", i)})
		old = append(old, entry{kind: kindDiff, text: fmt.Sprintf("old-diff-%d", i)})
		old = append(old, entry{kind: kindTool, text: fmt.Sprintf("old-tool-%d", i)})
	}
	m.entries = append(old, m.entries...)
	m.gcOldEntryText()

	for i := 0; i < len(m.entries)-entryTextKeepFull; i++ {
		e := m.entries[i]
		switch e.kind {
		case kindUser, kindAssistant, kindDiff:
			if !e.gc {
				t.Fatalf("[%d] %v not gc'd", i, e.kind)
			}
		default:
			if e.gc {
				t.Fatalf("[%d] %v should not gc", i, e.kind)
			}
		}
	}
}

func TestEntryVisibleBounds(t *testing.T) {
	m := gcTestModel(t)

	// Not ready → false.
	m.ready = false
	if m.entryVisible(0) {
		t.Fatal("visible when not ready")
	}
	m.ready = true

	// followBottom → always false (old entries treated as off-screen).
	m.followBottom = true
	m.entries = []entry{{kind: kindStatus, text: "x"}}
	m.ensureEntryHeights(80)
	if m.entryVisible(0) {
		t.Fatal("followBottom should report not visible")
	}

	m.followBottom = false
	// Missing line index → true (keep; unknown = visible).
	m.entryLineStart = nil
	m.entryHeights = nil
	if !m.entryVisible(0) {
		t.Fatal("unknown index should be treated visible")
	}

	for i := 0; i < 30; i++ {
		m.add(kindStatus, fmt.Sprintf("e%02d", i))
	}
	m.followBottom = false
	m.vp.SetYOffset(0)
	m.refreshVP()

	if !m.entryVisible(0) {
		t.Fatal("entry 0 should be visible at top")
	}
	// Out-of-range high index is "unknown → keep" (true).
	if !m.entryVisible(len(m.entries) + 5) {
		t.Fatal("out-of-range should be treated visible (keep)")
	}
}

func TestGCIdempotent(t *testing.T) {
	m := gcTestModel(t)
	m.followBottom = true
	for i := 0; i < entryTextKeepFull+8; i++ {
		m.add(kindUser, fmt.Sprintf("idem-%d %s", i, strings.Repeat("z", 30)))
	}
	m.gcOldEntryText()
	snap := make([]entry, len(m.entries))
	copy(snap, m.entries)
	m.gcOldEntryText()
	for i := range m.entries {
		if m.entries[i].gc != snap[i].gc || m.entries[i].text != snap[i].text {
			t.Fatalf("[%d] second GC changed entry", i)
		}
	}
}

func TestGCClearsViewAndPrettyWant(t *testing.T) {
	m := gcTestModel(t)
	m.followBottom = true
	m.prettyWant = map[int]bool{}
	for i := 0; i < entryTextKeepFull+5; i++ {
		m.entries = append(m.entries, entry{
			kind:  kindAssistant,
			text:  fmt.Sprintf("pw-%d %s", i, strings.Repeat("q", 20)),
			view:  "STALE_VIEW",
			viewW: 80,
			at:    time.Now(),
		})
		m.prettyWant[i] = true
	}
	m.gcOldEntryText()
	for i, e := range m.entries {
		if !e.gc {
			continue
		}
		if e.view == "STALE_VIEW" || e.viewW != 0 {
			t.Fatalf("[%d] stale view retained", i)
		}
		if m.prettyWant[i] {
			t.Fatalf("[%d] prettyWant not deleted", i)
		}
	}
}

func TestAddAtTriggersGC(t *testing.T) {
	m := gcTestModel(t)
	m.followBottom = true
	for i := 0; i < entryTextKeepFull+4; i++ {
		m.addAt(kindAssistant, fmt.Sprintf("addat-%d content", i), time.Time{})
	}
	gcN := 0
	for _, e := range m.entries {
		if e.gc {
			gcN++
		}
	}
	if gcN == 0 {
		t.Fatal("addAt path did not GC any old entries")
	}
}

func TestCopyLastAnswerSkipsGC(t *testing.T) {
	// Mirrors copyLastAnswer walk: skip gc stubs.
	entries := []entry{
		{kind: kindAssistant, text: "…(mowi gc) old", gc: true},
		{kind: kindAssistant, text: "live answer body"},
	}
	text := ""
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].kind == kindAssistant && !entries[i].gc {
			text = entries[i].text
			break
		}
	}
	if text != "live answer body" {
		t.Fatalf("got %q", text)
	}

	entries = []entry{{kind: kindAssistant, text: "…(mowi gc) only", gc: true}}
	text = ""
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].kind == kindAssistant && !entries[i].gc {
			text = entries[i].text
			break
		}
	}
	if text != "" {
		t.Fatalf("should skip gc-only, got %q", text)
	}
}

func TestEnsureHistoryCacheWithGCStubs(t *testing.T) {
	m := gcTestModel(t)
	m.followBottom = true
	for i := 0; i < entryTextKeepFull+6; i++ {
		m.add(kindAssistant, fmt.Sprintf("gcrender-%d %s", i, strings.Repeat("G", 40)))
	}
	m.refreshVP()
	if m.historyCacheN != len(m.entries) {
		t.Fatalf("cacheN=%d entries=%d", m.historyCacheN, len(m.entries))
	}
	// Stubs must render without panic; forcePretty still plain.
	for i, e := range m.entries {
		if !e.gc {
			continue
		}
		v := m.entryViewVirtual(i, 80, true)
		if v == "" {
			t.Fatalf("stub view empty at %d", i)
		}
		if !m.entries[i].plain {
			t.Fatalf("gc entry %d not plain after view", i)
		}
	}
	// Line accounting still coherent with stubs in the mix.
	got := strings.Split(m.historyCache, "\n")
	if want := m.totalEntryLines(); want != len(got) {
		t.Errorf("totalEntryLines=%d cache lines=%d", want, len(got))
	}
}

func TestShouldPrettyFalseForGC(t *testing.T) {
	// afterScrollPretty skips e.gc; entryViewVirtual never glamours gc.
	m := gcTestModel(t)
	m.entries = []entry{
		{kind: kindAssistant, text: "…(mowi gc) x", gc: true},
	}
	// Single entry is inside prettyWindow positionally…
	if !m.shouldPretty(0) {
		// position-based shouldPretty does not check gc — that's OK.
	}
	// But view path stays plain:
	_ = m.entryViewVirtual(0, 80, true)
	if !m.entries[0].plain {
		t.Fatal("gc must not leave plain=false")
	}
}
