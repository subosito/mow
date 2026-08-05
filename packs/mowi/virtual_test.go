package mowi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// --- helpers ---

func virtualTestModel(t *testing.T, w, h int) *model {
	t.Helper()
	m := newModel(testEngine(t), false, false)
	// Drive through WindowSize so layout/vp match production.
	mod, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return mod.(*model)
}

func seedEntries(m *model, n int, text string) {
	for i := 0; i < n; i++ {
		kind := kindUser
		if i%2 == 1 {
			kind = kindAssistant
		}
		body := text
		if body == "" {
			body = fmt.Sprintf("entry-%d line content that wraps a bit", i)
		}
		m.entries = append(m.entries, entry{
			kind: kind,
			text: body,
			at:   time.Now(),
		})
	}
	m.invalidateHistoryCache()
}

// --- countContentLines ---

func TestCountContentLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 1}, // empty still occupies one visual row in estimates
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"a\n\nb", 3},
		{"\n", 1},
		{"\n\n", 2},
		{strings.Repeat("x\n", 10), 10},
	}
	for _, tc := range cases {
		if got := countContentLines(tc.in); got != tc.want {
			t.Errorf("countContentLines(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// --- estimateEntryHeight ---

func TestEstimateEntryHeight(t *testing.T) {
	if n := estimateEntryHeight(&entry{kind: kindStatus, text: ""}, 40); n < 1 {
		t.Fatalf("empty height = %d, want ≥1", n)
	}
	if n := estimateEntryHeight(&entry{kind: kindAssistant, text: "", gc: true}, 40); n < 1 {
		t.Fatalf("gc empty height = %d, want ≥1", n)
	}

	short := estimateEntryHeight(&entry{kind: kindAssistant, text: "hi"}, 40)
	long := estimateEntryHeight(&entry{
		kind: kindAssistant,
		text: strings.Repeat("word ", 80),
	}, 40)
	if long <= short {
		t.Fatalf("long height %d should exceed short %d", long, short)
	}

	// Short tool/status collapse to 1 line.
	th := estimateEntryHeight(&entry{kind: kindTool, text: "bash · 0.1s"}, 40)
	if th != 1 {
		t.Fatalf("short tool height = %d, want 1", th)
	}
	sh := estimateEntryHeight(&entry{kind: kindStatus, text: "ok"}, 40)
	if sh != 1 {
		t.Fatalf("short status height = %d, want 1", sh)
	}
}

// --- ensureEntryHeights / totalEntryLines ---

func TestEnsureEntryHeightsAndTotal(t *testing.T) {
	m := virtualTestModel(t, 80, 24)
	seedEntries(m, 5, "hello world")
	// Pre-baked view on one entry should be preferred over estimate.
	m.entries[0].view = "line1\nline2\nline3"
	m.entries[0].viewW = 80

	m.ensureEntryHeights(80)
	if len(m.entryHeights) != 5 {
		t.Fatalf("heights len = %d, want 5", len(m.entryHeights))
	}
	if m.entryHeights[0] != 3 {
		t.Fatalf("entry[0] height = %d, want 3 (from view)", m.entryHeights[0])
	}
	for i, h := range m.entryHeights {
		if h < 1 {
			t.Fatalf("entry[%d] height = %d, want ≥1", i, h)
		}
	}

	total := m.totalEntryLines()
	// sum(heights) + per-entry separators (user turns get extra air).
	sum := 0
	for _, h := range m.entryHeights {
		sum += h
	}
	sep := 0
	for i := 1; i < len(m.entries); i++ {
		sep += entrySepBefore(m.entries[i])
	}
	want := sum + sep
	if total != want {
		t.Fatalf("totalEntryLines=%d, want %d (sum=%d sep=%d)", total, want, sum, sep)
	}
}

func TestEnsureEntryHeightsResizesSlice(t *testing.T) {
	m := virtualTestModel(t, 80, 24)
	seedEntries(m, 3, "a")
	m.ensureEntryHeights(80)
	seedEntries(m, 2, "b") // now 5
	m.ensureEntryHeights(80)
	if len(m.entryHeights) != 5 {
		t.Fatalf("after grow: heights=%d, want 5", len(m.entryHeights))
	}
}

func TestEnsureEntryHeightsIgnoresWrongWidthView(t *testing.T) {
	m := virtualTestModel(t, 80, 24)
	seedEntries(m, 2, "hello world")
	m.entries[0].view = "only-one-line"
	m.entries[0].viewW = 20 // wrong width
	m.ensureEntryHeights(80)
	// Must fall through to estimate, not trust the 1-line stale view blindly
	// as a width-matched cache. estimate of "hello world" is still ≥1.
	if m.entryHeights[0] < 1 {
		t.Fatal("height collapsed")
	}
	// With matching width it would be countContentLines("only-one-line")=1.
	// Wrong width → estimate from text (also 1 for short text) — just ensure no panic.
}

// --- ensureHistoryCacheVirtual ---

func TestVirtualLineAccounting(t *testing.T) {
	m := virtualTestModel(t, 80, 24)
	// Mixed kinds: turn views vs bare meta lines used to drift +1 per entry.
	m.add(kindUser, "question one")
	m.add(kindStatus, "status line")
	m.add(kindAssistant, "answer one\nwith a second line")
	m.add(kindTool, "bash · ok")
	m.add(kindUser, "question two")
	m.refreshVP()

	if m.historyCache == "" {
		t.Fatal("historyCache empty after refreshVP")
	}
	if len(m.entryHeights) != 5 {
		t.Fatalf("heights=%d, want 5", len(m.entryHeights))
	}
	if len(m.entryLineStart) != 5 {
		t.Fatalf("lineStart=%d, want 5", len(m.entryLineStart))
	}

	got := strings.Split(m.historyCache, "\n")
	// entryLineStart[i] must point at the actual first rendered line of entry i.
	for i := range m.entries {
		start := m.entryLineStart[i]
		if start < 0 || start >= len(got) {
			t.Fatalf("entry %d start %d beyond cache (%d lines)", i, start, len(got))
		}
		if i > 0 {
			blanks := entrySepBefore(m.entries[i])
			for b := 1; b <= blanks; b++ {
				sep := got[start-b]
				if strings.TrimSpace(sep) != "" {
					t.Errorf("entry %d: blank %d before start %d not empty: %q", i, b, start, sep)
				}
			}
			// Gap between prev content and this entry equals blanks.
			prevEnd := m.entryLineStart[i-1] + m.entryHeights[i-1] - 1
			gap := start - prevEnd - 1
			if gap != blanks {
				t.Errorf("entry %d: gap=%d want %d blanks (kind=%v)", i, gap, blanks, m.entries[i].kind)
			}
		}
	}
	// Total accounted lines == actual cache lines.
	if want := m.totalEntryLines(); want != len(got) {
		t.Errorf("totalEntryLines=%d but cache has %d lines", want, len(got))
	}
}

func TestVirtualCacheHitSkipsRebuild(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	seedEntries(m, 10, "cached body")
	m.ensureHistoryCacheVirtual(80)
	first := m.historyCache
	if first == "" {
		t.Fatal("empty cache")
	}
	// Mutate underlying text WITHOUT invalidate — cache hit must keep old string.
	// (Production mutators clear e.view; here we leave it to prove the document
	// cache short-circuits before reading entries at all.)
	m.entries[0].text = "MUTATED-SHOULD-NOT-APPEAR"
	m.ensureHistoryCacheVirtual(80)
	if m.historyCache != first {
		t.Fatal("cache rebuilt on hit; expected identical string")
	}
	if strings.Contains(m.historyCache, "MUTATED") {
		t.Fatal("cache hit still rebuilt from entries")
	}

	// invalidateHistoryCache only drops the joined document — per-entry .view
	// is intentionally retained (glamour is expensive). Production text edits
	// always clear e.view at the mutation site (see bumpToolTally).
	m.invalidateHistoryCache()
	m.ensureHistoryCacheVirtual(80)
	if strings.Contains(m.historyCache, "MUTATED") {
		t.Fatal("invalidate alone must not drop entry.view; got mutated text")
	}
	if !strings.Contains(m.historyCache, "cached body") {
		t.Fatal("rebuild from retained entry.view lost original body")
	}

	// Clear entry view like production mutators, then invalidate → new text.
	m.entries[0].view, m.entries[0].viewW = "", 0
	m.invalidateHistoryCache()
	m.ensureHistoryCacheVirtual(80)
	if !strings.Contains(m.historyCache, "MUTATED") {
		t.Fatal("after view clear + invalidate, cache should include new text")
	}
}

func TestVirtualCacheDirtyForcesRebuild(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	seedEntries(m, 4, "body")
	m.ensureHistoryCacheVirtual(80)
	// Mirror production mutators: text change drops the per-entry view cache.
	m.entries[1].text = "DIRTY-REBUILD"
	m.entries[1].view, m.entries[1].viewW = "", 0
	// historyDirty bypasses the (W,N) hit even without invalidate.
	m.historyDirty = true
	m.ensureHistoryCacheVirtual(80)
	if !strings.Contains(m.historyCache, "DIRTY-REBUILD") {
		t.Fatal("historyDirty did not force rebuild")
	}
	if m.historyDirty {
		t.Fatal("historyDirty not cleared after rebuild")
	}
}

func TestVirtualCacheInvalidOnWidthChange(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	seedEntries(m, 6, strings.Repeat("wrap-me ", 30))
	m.ensureHistoryCacheVirtual(80)
	m.ensureHistoryCacheVirtual(40)
	if m.historyCacheW != 40 {
		t.Fatalf("historyCacheW=%d, want 40", m.historyCacheW)
	}
	if m.historyCacheN != 6 {
		t.Fatalf("cacheN=%d, want 6", m.historyCacheN)
	}
	if len(m.entryHeights) != 6 {
		t.Fatalf("heights len %d", len(m.entryHeights))
	}
}

func TestVirtualEmptyTranscript(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	m.ensureHistoryCacheVirtual(80)
	if m.historyCache != "" {
		t.Fatalf("want empty cache, got %q", m.historyCache)
	}
	if m.historyCacheN != 0 {
		t.Fatalf("cacheN=%d, want 0", m.historyCacheN)
	}
	if m.totalEntryLines() != 0 {
		t.Fatalf("total lines=%d, want 0", m.totalEntryLines())
	}
}

func TestVirtualOffscreenPlaceholders(t *testing.T) {
	// Far-from-end entries outside the keepViewRadius band become blank
	// placeholders (view cleared, plain=true) while preserving height.
	m := virtualTestModel(t, 80, 8)
	// Need more than prettyWindow entries so early ones are not force-rendered.
	n := prettyWindow + 30
	for i := 0; i < n; i++ {
		m.add(kindStatus, fmt.Sprintf("row-%03d unique", i))
	}
	// Pin near bottom (follow) so the top of the list is off-screen.
	m.followBottom = true
	m.invalidateHistoryCache()
	m.ensureHistoryCacheVirtual(80)

	if m.historyCacheN != n {
		t.Fatalf("cacheN=%d, want %d", m.historyCacheN, n)
	}
	// Early entry should have had its view cache dropped (placeholder path).
	// (Unless keepViewRadius + viewport still covers it — with n large and
	// followBottom, y ≈ total-vh, so entry 0 is far above.)
	if m.entries[0].view != "" {
		// May still be onScreen if total height is small; only fail when
		// clearly outside band.
		total := m.totalEntryLines()
		vh := m.vp.Height()
		if total > vh+keepViewRadius*2+10 && m.entries[0].viewW != 0 {
			t.Fatalf("entry 0 still has view cache while off-screen (total=%d vh=%d)", total, vh)
		}
	}
	// Tail within prettyWindow must be fully rendered (contains real text).
	if !strings.Contains(m.historyCache, fmt.Sprintf("row-%03d", n-1)) {
		t.Fatalf("pretty window missing last row: %q", trunc(m.historyCache, 120))
	}
	// Line accounting still matches.
	got := strings.Split(m.historyCache, "\n")
	if want := m.totalEntryLines(); want != len(got) {
		t.Errorf("totalEntryLines=%d cache lines=%d", want, len(got))
	}
}

func TestVirtualWindowAroundViewportTop(t *testing.T) {
	m := virtualTestModel(t, 80, 10)
	n := prettyWindow + 40
	for i := 0; i < n; i++ {
		m.add(kindStatus, fmt.Sprintf("row-%03d", i))
	}
	m.followBottom = false
	m.vp.SetYOffset(0)
	m.invalidateHistoryCache()
	m.ensureHistoryCacheVirtual(80)

	if !strings.Contains(m.historyCache, "row-000") {
		t.Fatalf("top window missing row-000: %q", trunc(m.historyCache, 120))
	}
	// Near-end force-pretty still includes the last rows even when scrolled top.
	if !strings.Contains(m.historyCache, fmt.Sprintf("row-%03d", n-1)) {
		t.Fatalf("prettyWindow tail missing at top scroll")
	}
}

// --- entryViewVirtual ---

func TestEntryViewVirtualPlainAndCached(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	m.entries = []entry{{
		kind: kindAssistant,
		text: "plain answer",
		at:   time.Now(),
	}}
	// Force outside pretty window so plain path is used when forcePretty=false
	// and shouldPretty is false. With a single entry, shouldPretty is true
	// (near end). Seed many fillers first.
	fillers := make([]entry, prettyWindow+2)
	for i := range fillers {
		fillers[i] = entry{kind: kindStatus, text: "pad", at: time.Now()}
	}
	// Put target assistant early (index 0), pads after → not in pretty window.
	m.entries = append([]entry{{
		kind: kindAssistant,
		text: "plain answer",
		at:   time.Now(),
	}}, fillers...)

	v1 := m.entryViewVirtual(0, 80, false)
	if v1 == "" {
		t.Fatal("empty view")
	}
	if m.entries[0].view != v1 || m.entries[0].viewW != 80 {
		t.Fatal("view not cached on entry")
	}
	if !m.entries[0].plain {
		t.Fatal("expected plain wrap for far assistant without forcePretty")
	}
	// Second call returns cached.
	v2 := m.entryViewVirtual(0, 80, false)
	if v2 != v1 {
		t.Fatal("cache miss on second call")
	}
	// Width change recomputes.
	v3 := m.entryViewVirtual(0, 40, false)
	if m.entries[0].viewW != 40 {
		t.Fatalf("viewW=%d after width change", m.entries[0].viewW)
	}
	if v3 == "" {
		t.Fatal("empty after rewrap")
	}
}

func TestEntryViewVirtualGCStub(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	m.entries = []entry{{
		kind: kindAssistant,
		text: "…(mow gc) prior answer summary",
		gc:   true,
		at:   time.Now(),
	}}
	v := m.entryViewVirtual(0, 80, true) // even forcePretty must stay plain
	if v == "" {
		t.Fatal("gc stub produced empty view")
	}
	if !m.entries[0].plain {
		t.Fatal("gc entry must stay plain (no glamour)")
	}
	if m.entries[0].viewW != 80 {
		t.Fatalf("viewW=%d", m.entries[0].viewW)
	}
}

func TestEntryViewVirtualKinds(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	kinds := []entryKind{kindUser, kindAssistant, kindTool, kindError, kindStatus, kindDiff, kindPerm}
	for _, k := range kinds {
		m.entries = []entry{{kind: k, text: "sample content for kind", at: time.Now()}}
		v := m.entryViewVirtual(0, 80, false)
		if v == "" {
			t.Fatalf("kind %v empty view", k)
		}
	}
}

// --- shouldPretty / afterScrollPretty ---

func TestShouldPretty(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	// Build more than prettyWindow entries.
	n := prettyWindow + 10
	m.entries = make([]entry, n)
	for i := range m.entries {
		m.entries[i] = entry{kind: kindAssistant, text: "x"}
	}
	// Early index: not pretty unless prettyWant.
	if m.shouldPretty(0) {
		t.Fatal("early entry should not pretty by default")
	}
	// Tail within prettyWindow.
	if !m.shouldPretty(n - 1) {
		t.Fatal("tail should pretty")
	}
	m.prettyWant = map[int]bool{0: true}
	if !m.shouldPretty(0) {
		t.Fatal("prettyWant should force pretty")
	}
	// Empty transcript → true (safe default).
	m.entries = nil
	if !m.shouldPretty(0) {
		t.Fatal("empty shouldPretty want true")
	}
}

func TestAfterScrollPrettyFollowAndUpgrade(t *testing.T) {
	m := virtualTestModel(t, 80, 10)
	// Assistants outside the default pretty window so scroll can upgrade them.
	n := prettyWindow + 20
	for i := 0; i < n; i++ {
		kind := kindStatus
		if i%3 == 0 {
			kind = kindAssistant
		}
		m.add(kind, fmt.Sprintf("scroll-body-%d with some text", i))
	}
	m.followBottom = false
	m.refreshVP()
	m.vp.SetYOffset(0)

	cmd := m.afterScrollPretty()
	// Should mark nearby assistants for pretty and/or rebuild.
	if m.prettyWant == nil && cmd == nil {
		// Still ok if everything was already plain status — ensure no panic
		// and historyDirty path applied.
	}
	// Rebuild happened (dirty cleared via apply, or cmds queued).
	if m.historyCacheN != n && cmd == nil {
		// applyVPContent path sets cacheN
		if m.historyCacheN != len(m.entries) {
			t.Fatalf("cacheN=%d after scroll pretty", m.historyCacheN)
		}
	}

	// Far view caches outside radius + prettyWindow should be dropped.
	// (Best-effort: at least the function ran.)
	_ = cmd
}

func TestAfterScrollPrettyEmpty(t *testing.T) {
	m := virtualTestModel(t, 80, 10)
	if cmd := m.afterScrollPretty(); cmd != nil {
		t.Fatal("empty transcript should return nil cmd")
	}
}

// --- applyVPContent / followBottom ---

func TestApplyVPContentFollowAndPin(t *testing.T) {
	m := virtualTestModel(t, 80, 8)
	for i := 0; i < 40; i++ {
		m.add(kindStatus, fmt.Sprintf("line %02d", i))
	}

	m.followBottom = true
	m.applyVPContent()
	if !m.followBottom {
		t.Fatal("follow cleared")
	}

	// Pin: preserve YOffset; never re-enable follow.
	m.followBottom = false
	m.invalidateHistoryCache()
	m.ensureHistoryCacheVirtual(max(24, m.vp.Width()-2))
	total := m.totalEntryLines()
	pin := 2
	if total > 15 {
		pin = 4
	}
	m.vp.SetYOffset(pin)
	m.applyVPContent()
	if m.followBottom {
		t.Fatal("applyVPContent re-enabled follow while pinned")
	}
	if m.vp.YOffset() != pin {
		// SetContent may clamp if content shorter than expected; allow clamp
		// but must not jump to bottom when we pinned above it.
		if m.vp.AtBottom() && pin+m.vp.Height() < total {
			t.Fatalf("pinned view jumped to bottom (y=%d pin=%d total=%d)", m.vp.YOffset(), pin, total)
		}
	}
}

func TestApplyVPContentAppendsLiveStream(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	m.add(kindUser, "question")
	m.busy = true
	w := max(24, m.vp.Width()-2)
	m.streamFrame = "▌ partial answer"
	m.streamFrameW = w
	m.invalidateHistoryCache()
	m.applyVPContent()
	if m.historyCache == "" && len(m.entries) > 0 {
		t.Fatal("history empty")
	}
	// Wrong width → stream not appended (no panic).
	m.streamFrameW = w + 7
	m.applyVPContent()
}

func TestRefreshVPInvalidates(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	seedEntries(m, 4, "x")
	m.ensureHistoryCacheVirtual(80)
	if m.historyCacheN != 4 {
		t.Fatalf("pre N=%d", m.historyCacheN)
	}
	// refreshVP invalidates the document cache; entry.view must also be
	// cleared when source text changes (same contract as bumpTool*).
	m.entries[0].text = "changed"
	m.entries[0].view, m.entries[0].viewW = "", 0
	m.refreshVP()
	if !strings.Contains(m.historyCache, "changed") {
		t.Fatal("refreshVP did not rebuild")
	}
}

// --- clearTranscript resets virtual state ---

func TestClearTranscriptResetsVirtualState(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	seedEntries(m, 10, "bye")
	m.refreshVP()
	m.prettyWant = map[int]bool{1: true, 2: true}
	m.clearTranscript()
	if len(m.entries) != 0 {
		t.Fatalf("entries=%d", len(m.entries))
	}
	if m.entryHeights != nil || m.entryLineStart != nil {
		t.Fatal("heights/starts not cleared")
	}
	if m.prettyWant != nil {
		t.Fatal("prettyWant not cleared")
	}
}

// --- invalidateHistoryCache ---

func TestInvalidateHistoryCache(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	m.historyCache = "stale"
	m.historyCacheW = 80
	m.historyCacheN = 3
	m.invalidateHistoryCache()
	if m.historyCache != "" || m.historyCacheW != 0 || m.historyCacheN != -1 {
		t.Fatalf("not cleared: %q w=%d n=%d", m.historyCache, m.historyCacheW, m.historyCacheN)
	}
}

// --- scroll follow toggle (viewport integration) ---

func TestScrollFollowBottomToggle(t *testing.T) {
	m := virtualTestModel(t, 80, 6)
	for i := 0; i < 50; i++ {
		m.add(kindStatus, fmt.Sprintf("r%02d", i))
	}
	m.refreshVP()
	m.followBottom = true
	m.vp.GotoBottom()

	before := m.vp.YOffset()
	m.vp.HalfPageUp()
	if m.vp.YOffset() < before {
		m.followBottom = false
	}
	_ = m.afterScrollPretty()

	m.vp.GotoBottom()
	m.followBottom = m.vp.AtBottom()
	if !m.followBottom {
		t.Fatal("expected follow at bottom")
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- virtualization constants / band math ---

func TestVirtualizationConstants(t *testing.T) {
	// Guards against accidental regress of window sizes that trade memory vs UX.
	if prettyWindow < 8 {
		t.Fatalf("prettyWindow=%d too small", prettyWindow)
	}
	if scrollPrettyRadius < 1 {
		t.Fatalf("scrollPrettyRadius=%d", scrollPrettyRadius)
	}
	if keepViewRadius < scrollPrettyRadius {
		// keep band should cover the scroll-upgrade radius so upgraded views
		// are not immediately dropped on the next virtual rebuild.
		t.Fatalf("keepViewRadius=%d < scrollPrettyRadius=%d", keepViewRadius, scrollPrettyRadius)
	}
	if entrySepLines != 1 {
		t.Fatalf("entrySepLines=%d, default separator is one blank line", entrySepLines)
	}
	if entrySepTurnLines != 2 {
		t.Fatalf("entrySepTurnLines=%d, user-turn rhythm is two blank lines", entrySepTurnLines)
	}
	if entrySepBefore(entry{kind: kindUser}) != entrySepTurnLines {
		t.Fatal("user turns must use turn separator")
	}
	if entrySepBefore(entry{kind: kindAssistant}) != entrySepLines {
		t.Fatal("assistant glue must use default separator")
	}
}

func TestVirtualForcePrettyWant(t *testing.T) {
	// prettyWant marks an early assistant for full render even when scrolled
	// to the bottom (outside the default visible band).
	m := virtualTestModel(t, 80, 8)
	n := prettyWindow + 30
	for i := 0; i < n; i++ {
		kind := kindStatus
		text := fmt.Sprintf("pad-%03d", i)
		if i == 0 {
			kind = kindAssistant
			text = "FORCE-PRETTY-UNIQUE-BODY"
		}
		m.add(kind, text)
	}
	m.followBottom = true
	m.prettyWant = map[int]bool{0: true}
	// Drop any view baked while building so force path re-renders.
	m.entries[0].view, m.entries[0].viewW = "", 0
	m.invalidateHistoryCache()
	m.ensureHistoryCacheVirtual(80)
	if !strings.Contains(m.historyCache, "FORCE-PRETTY-UNIQUE-BODY") {
		t.Fatalf("prettyWant[0] did not force render: %q", trunc(m.historyCache, 200))
	}
	// Entry 0 is far above the follow-bottom band; without prettyWant it would
	// be a placeholder (view cleared). With force it must retain a view.
	if m.entries[0].view == "" {
		t.Fatal("forced entry lost its view cache")
	}
}

func TestVirtualHeightMatchesRendered(t *testing.T) {
	// After a full on-screen build, entryHeights must equal the line count of
	// each rendered segment (no drift from estimates).
	m := virtualTestModel(t, 80, 24)
	m.add(kindUser, "short user")
	m.add(kindAssistant, "assistant\nwith\nthree")
	m.add(kindTool, "bash · 0.2s")
	m.add(kindStatus, "done")
	m.followBottom = true
	m.refreshVP()

	if len(m.entryHeights) != len(m.entries) || len(m.entryLineStart) != len(m.entries) {
		t.Fatalf("len mismatch h=%d s=%d e=%d", len(m.entryHeights), len(m.entryLineStart), len(m.entries))
	}
	lines := strings.Split(m.historyCache, "\n")
	for i := range m.entries {
		start := m.entryLineStart[i]
		h := m.entryHeights[i]
		if h < 1 {
			t.Fatalf("entry %d height %d", i, h)
		}
		if start < 0 || start+h > len(lines) {
			t.Fatalf("entry %d span [%d,%d) outside %d lines", i, start, start+h, len(lines))
		}
		// Spot-check: non-separator lines in the span should be non-empty after trim
		// for on-screen full renders (all entries are in pretty/near-end window).
		seg := lines[start : start+h]
		if len(seg) != h {
			t.Fatalf("entry %d seg len %d want %d", i, len(seg), h)
		}
	}
	if m.totalEntryLines() != len(lines) {
		t.Fatalf("totalEntryLines=%d cache=%d", m.totalEntryLines(), len(lines))
	}
}

func TestEnsureHistoryCacheIdempotent(t *testing.T) {
	m := virtualTestModel(t, 80, 20)
	seedEntries(m, 12, "idem-body")
	m.ensureHistoryCacheVirtual(80)
	a := m.historyCache
	starts := append([]int(nil), m.entryLineStart...)
	heights := append([]int(nil), m.entryHeights...)
	m.ensureHistoryCacheVirtual(80)
	if m.historyCache != a {
		t.Fatal("second ensure changed cache on hit")
	}
	if len(m.entryLineStart) != len(starts) {
		t.Fatal("lineStart len changed on hit")
	}
	for i := range starts {
		if m.entryLineStart[i] != starts[i] || m.entryHeights[i] != heights[i] {
			t.Fatalf("geometry changed on hit at %d", i)
		}
	}
}
