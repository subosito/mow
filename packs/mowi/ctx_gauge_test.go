package mowi

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The gauge is a pure function of the ratio: same inputs, same cells. It must
// track the level thresholds formatContextPctLevel already uses, so the bar and
// the number can never disagree about pressure.
func TestCtxGaugeFillTracksRatio(t *testing.T) {
	m := &model{theme: newTheme()}
	cases := []struct {
		name      string
		used      int
		window    int
		wantFull  int
		wantEmpty int
	}{
		{"empty_ish", 1, 1000, 0, ctxGaugeWidth},
		{"half", 500, 1000, ctxGaugeWidth / 2, ctxGaugeWidth / 2},
		{"full", 1000, 1000, ctxGaugeWidth, 0},
		{"over_window_clamps", 5000, 1000, ctxGaugeWidth, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := m.ctxGauge(tc.used, tc.window)
			if bar == "" {
				t.Fatalf("gauge empty for used=%d window=%d", tc.used, tc.window)
			}
			plain := stripANSI(bar)
			full := strings.Count(plain, string(glyphGaugeFull))
			empty := strings.Count(plain, string(glyphGaugeEmpty))
			if full+empty != ctxGaugeWidth {
				t.Fatalf("gauge cells = %d+%d, want %d total: %q", full, empty, ctxGaugeWidth, plain)
			}
			if full != tc.wantFull || empty != tc.wantEmpty {
				t.Fatalf("fill=%d empty=%d, want %d/%d (%q)", full, empty, tc.wantFull, tc.wantEmpty, plain)
			}
		})
	}
}

// Unusable ratios yield no bar at all — callers fall back to the numeric label
// rather than rendering a misleading empty gauge.
func TestCtxGaugeSuppressedWhenUnknown(t *testing.T) {
	m := &model{theme: newTheme()}
	for _, tc := range []struct{ used, window int }{
		{0, 1000}, {-5, 1000}, {100, 0}, {100, -1}, {0, 0},
	} {
		if got := m.ctxGauge(tc.used, tc.window); got != "" {
			t.Fatalf("used=%d window=%d: want no gauge, got %q", tc.used, tc.window, stripANSI(got))
		}
	}
}

// The gauge occupies exactly ctxGaugeWidth columns regardless of ratio. The
// header is a hand-packed, priority-dropped chip line whose width math assumes
// stable chip widths — a gauge that changed width with the ratio would make the
// drop order jitter frame to frame.
func TestCtxGaugeWidthStable(t *testing.T) {
	m := &model{theme: newTheme()}
	want := -1
	for used := 1; used <= 1000; used += 37 {
		bar := m.ctxGauge(used, 1000)
		w := lipgloss.Width(bar)
		if want < 0 {
			want = w
		}
		if w != want {
			t.Fatalf("used=%d: gauge width %d, want stable %d", used, w, want)
		}
	}
	if want != ctxGaugeWidth {
		t.Fatalf("gauge width %d, want ctxGaugeWidth=%d", want, ctxGaugeWidth)
	}
}

// Safety posture outranks the gauge. On a narrow terminal the bar must be
// suppressed so write/shell/ask chips keep their columns — the gauge is a
// convenience, the posture is not.
func TestCtxGaugeSuppressedOnNarrowHeader(t *testing.T) {
	if ctxGaugeMinWidth <= minTermWidth {
		t.Fatalf("gauge gate %d must sit above the minimum usable width %d",
			ctxGaugeMinWidth, minTermWidth)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && (r == 'm' || r == 'K'):
			inEsc = false
		case !inEsc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
