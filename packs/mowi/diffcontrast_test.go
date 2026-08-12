package mowi

import (
	"fmt"
	"testing"
)

// Diff rows must read as bands, not as tinted text. The failure this guards
// against is not a crash: it is a diff that renders technically-correctly and
// is unusable, which no snapshot or grid test catches because every cell has
// exactly the color it was told to have.
//
// The floors are expressed as contrast ratios rather than mix ratios on
// purpose. A fixed mix lands very differently depending on how far a theme's
// accent already sits from its surface, so only the measured result is worth
// asserting.

// diffPalettes are the palettes worth checking: the shipped defaults plus
// deliberately hostile ones. The hostile cases matter because users write
// custom themes, and a palette whose add/del is nearly its own surface cannot
// be fixed by washing accent into surface at any ratio.
func diffPalettes() []struct {
	name string
	dark bool
	p    palette
} {
	return []struct {
		name string
		dark bool
		p    palette
	}{
		{"default-dark", true, defaultPalette(true)},
		{"default-light", false, defaultPalette(false)},
		{
			"low-contrast-light", false,
			palette{fg: "#333333", userBg: "#eeeeee", border: "#dddddd",
				add: "#dfe8df", del: "#e8dfdf"},
		},
		{
			"low-contrast-dark", true,
			palette{fg: "#eeeeee", userBg: "#202020", border: "#333333",
				add: "#2a332a", del: "#332a2a"},
		},
		{
			// No userBg: the resolver must fall back to border, then to a
			// built-in surface, without producing an empty background.
			"no-surface", true,
			palette{fg: "#eeeeee", add: "#4ADE80", del: "#F87171"},
		},
	}
}

func TestDiffRowBandIsVisible(t *testing.T) {
	for _, c := range diffPalettes() {
		base := c.p.userBg
		if base == "" {
			base = c.p.border
		}
		if base == "" {
			base = "#1e1e2e"
			if !c.dark {
				base = "#f3f4f6"
			}
		}
		for _, d := range []struct{ kind, accent string }{
			{"add", c.p.add}, {"del", c.p.del},
		} {
			t.Run(fmt.Sprintf("%s/%s", c.name, d.kind), func(t *testing.T) {
				bg := resolveDiffBg("", d.accent, c.p, c.dark)
				if bg == "" {
					t.Fatal("no diff background derived; the row would be unstyled")
				}
				if got := contrastRatio(bg, base); got < minDiffBandContrast {
					t.Errorf("band contrast %.2f < %.2f (bg %s on surface %s) — the row will not read as a block",
						got, minDiffBandContrast, bg, base)
				}
				// The row's own text must survive the stronger band.
				fg := diffFgOn(d.accent, bg, c.dark)
				if got := contrastRatio(fg, bg); got < minDiffTextContrast {
					t.Errorf("text contrast %.2f < %.2f (fg %s on bg %s) — band strengthened past legibility",
						got, minDiffTextContrast, fg, bg)
				}
			})
		}
	}
}

// An explicit add_bg/del_bg is the author's decision, not a starting point.
// Nudging it toward a contrast floor would silently override a deliberate
// choice — including one made for a display we cannot measure.
func TestDiffBgOverrideIsVerbatim(t *testing.T) {
	p := palette{userBg: "#202020", add: "#4ADE80", addBg: "#123412", del: "#F87171", delBg: "#341212"}
	if got := resolveDiffBg(p.addBg, p.add, p, true); got != p.addBg {
		t.Errorf("add override = %s, want %s verbatim", got, p.addBg)
	}
	if got := resolveDiffBg(p.delBg, p.del, p, true); got != p.delBg {
		t.Errorf("del override = %s, want %s verbatim", got, p.delBg)
	}
	// Whitespace-only is not an override.
	if got := resolveDiffBg("   ", p.add, p, true); got == "   " {
		t.Error("blank override treated as a color")
	}
}

// Add and delete must stay distinguishable from each other, not merely from
// the surface. A theme that renders both as the same gray band is readable and
// still wrong.
func TestDiffAddAndDelDiffer(t *testing.T) {
	for _, c := range diffPalettes() {
		t.Run(c.name, func(t *testing.T) {
			addBg := resolveDiffBg("", c.p.add, c.p, c.dark)
			delBg := resolveDiffBg("", c.p.del, c.p, c.dark)
			if addBg == delBg {
				t.Errorf("add and del share background %s", addBg)
			}
		})
	}
}

// diffFgOn must leave a already-legible accent alone: rewriting a color that
// was fine would drift every theme's palette for no reason.
func TestDiffFgUnchangedWhenLegible(t *testing.T) {
	// Bright green on a dark band is already well past the floor.
	const accent, bg = "#4ADE80", "#123412"
	if contrastRatio(accent, bg) < minDiffTextContrast {
		t.Fatal("fixture is not actually legible; pick another pair")
	}
	if got := diffFgOn(accent, bg, true); got != accent {
		t.Errorf("diffFgOn rewrote a legible accent: %s -> %s", accent, got)
	}
}

func TestDiffFgHandlesEmptyInput(t *testing.T) {
	if got := diffFgOn("", "#123412", true); got != "" {
		t.Errorf("empty accent = %q, want empty", got)
	}
	if got := diffFgOn("#4ADE80", "", true); got != "#4ADE80" {
		t.Errorf("empty bg = %q, want the accent unchanged", got)
	}
}

// The shipped defaults should clear the floor. Dark has natural headroom from
// saturated accents; light sits nearer the floor after the wash, so the gate
// is the floor itself — reporting "too soft" again means the floor dropped.
func TestDefaultPalettesHaveHeadroom(t *testing.T) {
	for _, dark := range []bool{true, false} {
		p := defaultPalette(dark)
		want := minDiffBandContrast
		if dark {
			// Saturated accents clear the floor with a little air; del is the
			// tighter of the pair on the default palette.
			want = 2.05
		}
		for _, d := range []struct{ kind, accent string }{{"add", p.add}, {"del", p.del}} {
			bg := resolveDiffBg("", d.accent, p, dark)
			if got := contrastRatio(bg, p.userBg); got < want {
				t.Errorf("default dark=%v %s band contrast %.2f < %.2f", dark, d.kind, got, want)
			}
		}
	}
}

// Stronger bands must still leave accent body text legible and must not
// collapse add/del into one identical wash (direction would vanish).
func TestStrongerDiffBandsStayLegible(t *testing.T) {
	for _, c := range diffPalettes() {
		t.Run(c.name, func(t *testing.T) {
			addBg := resolveDiffBg("", c.p.add, c.p, c.dark)
			delBg := resolveDiffBg("", c.p.del, c.p, c.dark)
			base := c.p.userBg
			if base == "" {
				base = c.p.border
			}
			if base == "" {
				if c.dark {
					base = "#1e1e2e"
				} else {
					base = "#f3f4f6"
				}
			}
			if got := contrastRatio(addBg, base); got < minDiffBandContrast {
				t.Errorf("add band %.2f < floor %.2f", got, minDiffBandContrast)
			}
			if got := contrastRatio(delBg, base); got < minDiffBandContrast {
				t.Errorf("del band %.2f < floor %.2f", got, minDiffBandContrast)
			}
			if addBg == delBg {
				t.Errorf("add and del share background %s", addBg)
			}
			for _, d := range []struct{ kind, accent, bg string }{
				{"add", c.p.add, addBg},
				{"del", c.p.del, delBg},
			} {
				fg := diffFgOn(d.accent, d.bg, c.dark)
				if got := contrastRatio(fg, d.bg); got < minDiffTextContrast {
					t.Errorf("%s text %.2f < floor %.2f (fg %s on %s)",
						d.kind, got, minDiffTextContrast, fg, d.bg)
				}
			}
		})
	}
}
