package mowi

import (
	"testing"

	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

// theme.name accepts any chroma style, so the diff colours mow derives are
// only as good as what an arbitrary third-party style happens to declare.
// These tests treat the whole chroma catalogue as untrusted input.

// chromaSample is a spread of styles that disagree about how diff colours are
// encoded: foreground-carrying (monokai, nord), background-carrying (gruvbox),
// light, dark, and deliberately colourless (bw, xcode).
var chromaSample = []string{
	"monokai", "nord", "dracula", "catppuccin-mocha", "gruvbox",
	"github", "github-dark", "solarized-dark", "solarized-light",
	"onedark", "vim", "emacs", "pygments", "rrt", "tango",
	"xcode", "algol", "bw",
}

// Add and delete must never render as the same band. A diff that shows a line
// changed but not in which direction is worse than no colour at all, and it
// fails silently: every cell has exactly the colour it was told to have.
func TestChromaDiffBandsAlwaysDiffer(t *testing.T) {
	for _, name := range chromaSample {
		t.Run(name, func(t *testing.T) {
			p, dark, ok := paletteFromChroma(name)
			if !ok {
				t.Skipf("%s is not a chroma style in this build", name)
			}
			addBg := resolveDiffBg("", p.add, p, dark)
			delBg := resolveDiffBg("", p.del, p, dark)
			if addBg == "" || delBg == "" {
				t.Fatalf("empty diff background (add %q, del %q)", addBg, delBg)
			}
			if addBg == delBg {
				t.Errorf("add and del bands identical (%s); direction of change is invisible", addBg)
			}
			// Bands are washes of the accents, so they sit closer together
			// than the accents themselves — measuring them against the accent
			// threshold would reject palettes whose colours are fine. What
			// matters is that the two backgrounds are separable at all; the
			// tinted line numbers and the +/− glyph carry the direction
			// unambiguously, so the band only has to not read as one block.
			if d := colorDistance(addBg, delBg); d < minDiffBandSeparation {
				t.Errorf("bands too close: distance %.1f < %.1f (%s vs %s)",
					d, minDiffBandSeparation, addBg, delBg)
			}
		})
	}
}

// gruvbox is the reason diffAccentFrom exists: it encodes diff colours as
// backgrounds with the text left at the page colour, so reading .Colour alone
// returned the page background for both add and del.
func TestChromaBackgroundCarriedDiffColors(t *testing.T) {
	p, dark, ok := paletteFromChroma("gruvbox")
	if !ok {
		t.Skip("gruvbox unavailable")
	}
	s := chromastyles.Get("gruvbox")
	wantAdd := s.Get(chroma.GenericInserted).Background.String()
	wantDel := s.Get(chroma.GenericDeleted).Background.String()

	if p.add != wantAdd {
		t.Errorf("add = %s, want the style's inserted background %s", p.add, wantAdd)
	}
	if p.del != wantDel {
		t.Errorf("del = %s, want the style's deleted background %s", p.del, wantDel)
	}
	// And the derived bands must survive the round trip.
	if resolveDiffBg("", p.add, p, dark) == resolveDiffBg("", p.del, p, dark) {
		t.Error("gruvbox bands collapsed back together")
	}
}

// A style that carries its diff colours on the foreground must keep using
// them: the background-channel fix must not override styles that were already
// correct. Pastel pairs (nord) are replaced later by vividDiffAccents.
func TestChromaForegroundDiffColorsPreserved(t *testing.T) {
	for _, name := range []string{"monokai", "nord", "github-dark"} {
		t.Run(name, func(t *testing.T) {
			p, _, ok := paletteFromChroma(name)
			if !ok {
				t.Skipf("%s unavailable", name)
			}
			s := chromastyles.Get(name)
			wantAdd := s.Get(chroma.GenericInserted).Colour.String()
			if p.add != wantAdd {
				t.Errorf("add = %s, want the style's own %s", p.add, wantAdd)
			}
		})
	}
}

// Pastel chrome themes (mocha, nord) keep their own add/del accents.
// Flashdiff-style sunk bands make those pastels readable; replacing them
// with neon green/red was the previous workaround and looked off-theme.
func TestPastelChromaStylesKeepOwnAccents(t *testing.T) {
	for _, name := range []string{"catppuccin-mocha", "nord"} {
		t.Run(name, func(t *testing.T) {
			p, dark, ok := paletteFromChroma(name)
			if !ok {
				t.Skipf("%s unavailable", name)
			}
			s := chromastyles.Get(name)
			wantAdd := s.Get(chroma.GenericInserted).Colour.String()
			wantDel := s.Get(chroma.GenericDeleted).Colour.String()
			if p.add != wantAdd || p.del != wantDel {
				t.Errorf("add/del = %s/%s, want chroma %s/%s", p.add, p.del, wantAdd, wantDel)
			}
			addBg := resolveDiffBg("", p.add, p, dark)
			delBg := resolveDiffBg("", p.del, p, dark)
			if colorDistance(addBg, delBg) < minDiffBandSeparation {
				t.Errorf("sunk bands too close: %s vs %s", addBg, delBg)
			}
			if contrastRatio(diffFgOn(p.add, addBg, dark), addBg) < minDiffTextContrast {
				t.Errorf("%s add text unreadable on %s", name, addBg)
			}
			if contrastRatio(diffFgOn(p.del, delBg, dark), delBg) < minDiffTextContrast {
				t.Errorf("%s del text unreadable on %s", name, delBg)
			}
		})
	}
}

// Styles with no diff colours at all fall back to conventional green/red
// rather than to two identical bands.
func TestChromaColorlessStylesFallBack(t *testing.T) {
	for _, name := range []string{"bw", "xcode"} {
		t.Run(name, func(t *testing.T) {
			p, dark, ok := paletteFromChroma(name)
			if !ok {
				t.Skipf("%s unavailable", name)
			}
			wantAdd, wantDel := fallbackDiffAccents(dark)
			if p.add != wantAdd || p.del != wantDel {
				t.Errorf("add/del = %s/%s, want fallback %s/%s", p.add, p.del, wantAdd, wantDel)
			}
		})
	}
}

// distinctAccents must judge by hue, not luminance. WCAG contrast is a
// luminance ratio: github-dark's green and salmon score 1.009 against each
// other, so a contrast-based test would discard a perfectly readable palette.
func TestDistinctAccentsSeesHueNotLuminance(t *testing.T) {
	const green, salmon = "#56d364", "#ffa198"
	if contrastRatio(green, salmon) >= 1.2 {
		t.Fatal("fixture no longer demonstrates the luminance blind spot")
	}
	if !distinctAccents(green, salmon) {
		t.Error("green vs salmon judged indistinct; the metric is luminance-blind again")
	}
	// Identical and near-identical colours must still be rejected.
	if distinctAccents("#24292e", "#24292e") {
		t.Error("identical accents accepted")
	}
	if distinctAccents("#24292e", "#25292e") {
		t.Error("near-identical accents accepted")
	}
	if distinctAccents("", "#24292e") || distinctAccents("#24292e", "") {
		t.Error("empty accent accepted")
	}
}

// Every chroma style must produce a usable palette, not just the sampled ones.
// This is the broad sweep: it will not catch subtle ugliness, but it does
// catch a style that yields empty or colliding diff colours.
func TestAllChromaStylesProduceUsableDiffColors(t *testing.T) {
	names := chromastyles.Names()
	if len(names) == 0 {
		t.Skip("no chroma styles registered")
	}
	for _, name := range names {
		p, dark, ok := paletteFromChroma(name)
		if !ok {
			continue
		}
		if p.add == "" || p.del == "" {
			t.Errorf("%s: empty diff accent (add %q, del %q)", name, p.add, p.del)
			continue
		}
		addBg := resolveDiffBg("", p.add, p, dark)
		delBg := resolveDiffBg("", p.del, p, dark)
		if addBg == delBg {
			t.Errorf("%s: add and del bands identical (%s)", name, addBg)
		}
	}
}
