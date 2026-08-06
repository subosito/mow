package mowi

import (
	"fmt"
	"image/color"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

// Theme is intentionally quiet: readable transcript first, chrome second.
type theme struct {
	name   string // resolved name: default, or a chroma style name
	Accent lipgloss.Style
	Muted  lipgloss.Style
	Error  lipgloss.Style
	Warn   lipgloss.Style
	Header lipgloss.Style
	// Text is plain foreground (no pad/bold) for legible inline chrome like the
	// header model name — Header carries padding that breaks inline joins.
	Text     lipgloss.Style
	Status   lipgloss.Style
	Input    lipgloss.Style
	InputFoc lipgloss.Style
	Box      lipgloss.Style
	Title    lipgloss.Style
	// RoleUserBar / RoleAsstBar color the role gutter (no text labels).
	RoleUserBar lipgloss.Style
	RoleAsstBar lipgloss.Style
	// RoleUserBg soft fills the user bubble so "you" is visual, not a label.
	RoleUserBg lipgloss.Style
	// StampUser is the inline clock at the start of a user block (muted on
	// the block background — no separate timestamp row).
	StampUser lipgloss.Style
	// Diff styles: code-review card (tinted rows + gutter), not raw git.
	DiffAdd  lipgloss.Style // + line (fg on soft bg when set)
	DiffDel  lipgloss.Style // − line
	DiffMeta lipgloss.Style // hunk headers / stats
	DiffNum  lipgloss.Style // line-number gutter
	DiffCtx  lipgloss.Style // unchanged context line
	Sep      lipgloss.Style
	// SlashCmd colors /commands in the input field.
	SlashCmd lipgloss.Style
	// palette is the hex token set shared by chrome and glamour markdown.
	palette palette
	// mdDark: dark document base (a dark chroma style forces this).
	mdDark bool
	// chromaStyle is the chroma theme name for fenced code (e.g. monokai).
	// Empty → chroma colors derived from palette.
	chromaStyle string
}

// Role gutter: thin bar + breathing space (agent transcript, not chat bubbles).
// roleGutterW must match the rendered prefix width for wrap math.
const (
	roleBar     = "▎"
	rolePad     = "  " // spaces after the bar
	roleGutterW = 3    // width of roleBar + rolePad
)

// Shared glyph vocabulary — one place so tool/status/error/header chrome reads
// consistently instead of each call site inventing its own symbol.
const (
	glyphBrand   = "◇" // header/idle marker (matches reduced-peer-bion spinner)
	glyphTool    = "⚙" // tool activity
	glyphError   = "✕" // failed tool / error line
	glyphCaret   = "▋" // live stream caret
	glyphArrow   = "→" // model/perm transitions
	glyphBullet  = "·" // status / separator dot
	glyphWarn    = "▲" // safety-relevant state (write/shell, permission)
	glyphWelcome = "◈" // welcome splash mark
	glyphPeer    = "⇄" // delegated peer spend (true-total chip)
	glyphMore    = "⋯" // collapsed / elided content (peer live summary)
	glyphSelect  = "⛶" // select mode: mouse released to the terminal
)

// palette is fixed hex colors (no AdaptiveColor probes).
type palette struct {
	fg, muted, accent, user, userBg          string
	err, warn, border, add, del, meta, slash string
	// Optional soft row washes for review-style diffs. Empty → derived from
	// add/del mixed into userBg so every theme (chroma name, custom
	// colors) keeps a uniform look without hardcoded green/red panels.
	addBg, delBg string
}

// defaultPalette matches the original adaptive dark side (quiet gray + indigo).
func defaultPalette(dark bool) palette {
	if dark {
		return palette{
			fg:     "#E5E7EB",
			muted:  "#9CA3AF",
			accent: "#A5B4FC",
			user:   "#6EE7B7",
			userBg: "#2A2A2E", // neutral dark gray block for user prompts
			err:    "#FCA5A5",
			warn:   "#FCD34D",
			border: "#374151",
			add:    "#4ADE80",
			del:    "#F87171",
			meta:   "#93C5FD",
			slash:  "#FBBF24",
		}
	}
	return palette{
		fg:     "#111827",
		muted:  "#6B7280",
		accent: "#4F46E5",
		user:   "#047857",
		userBg: "#E9E9EC", // neutral light gray block for user prompts
		err:    "#B91C1C",
		warn:   "#B45309",
		border: "#E5E7EB",
		add:    "#047857",
		del:    "#B91C1C",
		meta:   "#1D4ED8",
		slash:  "#C2410C", // orange-700: slash amber was identical to warn
	}
}

// ThemeConfig is extensions.tui.theme (YAML). One theme drives both the frame
// (chrome) and the markdown renderer — the palette is the single source of
// truth, so a full custom theme is just `colors` with every token set.
//
//	theme:
//	  name: catppuccin-mocha  # default | any chroma style name
//	                          # (catppuccin-mocha, dracula, nord, gruvbox, …):
//	                          # a chroma name derives the whole palette from it
//	                          # and light/dark is auto-detected.
//	  colors:            # palette overrides (drive chrome AND markdown/code)
//	    fg: "#E5E7EB"      muted: "#9CA3AF"   accent: "#A5B4FC"
//	    user: "#6EE7B7"    user_bg: "#2A2A2E" border: "#374151"
//	    error: "#FCA5A5"   warn: "#FCD34D"    slash: "#FBBF24"
//	    diff_add: "#4ADE80" diff_del: "#F87171" diff_meta: "#93C5FD"
//	    diff_add_bg: "#14532D"  # optional; default = wash of diff_add on user_bg
//	    diff_del_bg: "#7F1D1D"
//	  code: monokai      # optional named chroma style for fenced code only;
//	                     # empty = same as name (chroma), else palette-derived
type ThemeConfig struct {
	Name   string            `yaml:"name"`
	Colors map[string]string `yaml:"colors"`
	Code   string            `yaml:"code"`
}

// NormalizeThemeName resolves a configured theme name to what will actually be
// used: the literal preset "default", or a recognized chroma style name.
// Unknown values resolve to "default" — the same fallback newThemeFrom takes,
// so callers can report the effective theme without duplicating that logic.
//
// Note this is the *preset* name, not DefaultThemeName: an unconfigured theme
// resolves to DefaultThemeName elsewhere, while an explicitly bad value falls
// back to the built-in palette.
//
// There is one curated preset now. monokai used to be a second, hand-written
// palette, but chroma ships a monokai style, so the special case was a second
// implementation of a name the generic path already handles.
func NormalizeThemeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "default" {
		return "default"
	}
	if knownChromaStyle(s) {
		return s
	}
	return "default"
}

// knownChromaStyle reports whether name is a registered chroma style
// (the catalog at xyproto.github.io/splash: catppuccin-mocha, dracula, nord…).
func knownChromaStyle(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, n := range chromastyles.Names() {
		if strings.ToLower(n) == name {
			return true
		}
	}
	return false
}

// resolveChromaStyle validates theme.code against the chroma style registry.
// Empty (the default) means derive code colors from the palette. An unknown
// name is ignored with a warning rather than silently painting a fallback.
func resolveChromaStyle(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	if knownChromaStyle(name) {
		return name
	}
	slog.Warn("mowi: theme.code ignored (unknown chroma style)", "value", name)
	return ""
}

// paletteFromChroma derives mowi's semantic palette from a chroma syntax style,
// so `theme.name: catppuccin-mocha` themes the whole UI (frame + markdown) from
// one name. Chrome roles map to the nearest syntax token; missing tokens fall
// back to sensible derivations rather than a flat foreground. ok=false for an
// unknown style.
func paletteFromChroma(name string) (p palette, dark, ok bool) {
	if !knownChromaStyle(name) {
		return palette{}, false, false
	}
	s := chromastyles.Get(name)
	if s == nil {
		return palette{}, false, false
	}
	bg := s.Get(chroma.Background)
	hexOr := func(c chroma.Colour, fb string) string {
		if c.IsSet() {
			return c.String()
		}
		return fb
	}
	tok := func(tt chroma.TokenType, fb string) string {
		if e := s.Get(tt); e.Colour.IsSet() {
			return e.Colour.String()
		}
		return fb
	}
	background := hexOr(bg.Background, "#1e1e2e")
	fg := hexOr(bg.Colour, "#e5e7eb")
	if e := s.Get(chroma.Text); e.Colour.IsSet() {
		fg = e.Colour.String()
	}
	// Dark when the background is dark; if the style declares no background,
	// infer from the foreground (light text ⇒ dark theme).
	if bg.Background.IsSet() {
		dark = bg.Background.Brightness() < 0.5
	} else {
		dark = hexBrightness(fg) > 0.5
	}
	// Fallback ink must match the theme's polarity: #e5e7eb (near-white) is
	// right for dark themes but invisible as chrome text on light ones.
	// github-style palettes hit chroma's broken "#-00001" Text sentinel
	// (reads as unset), so the fallback above actually runs — correct it.
	if !dark && !s.Get(chroma.Text).Colour.IsSet() {
		fg = "#24292e"
	}
	dimFg := mixHex(fg, background, 0.45) // muted text when the style has no comment color
	// Diff accents: some styles (monokai, nord) colour the *text* of an
	// inserted line, others (gruvbox) colour its *background* and leave the
	// text at the page background. Reading only .Colour on the latter yields
	// the page background for both add and del — two identical, invisible
	// bands. Prefer whichever channel the style actually used.
	addAccent := diffAccentFrom(s, chroma.GenericInserted, background,
		tok(chroma.NameFunction, fg))
	delAccent := diffAccentFrom(s, chroma.GenericDeleted, background,
		tok(chroma.Error, fg))
	// Some styles genuinely do not distinguish insertions from deletions
	// (xcode and bw paint both in the body colour; solarized-light uses one
	// magenta for each). Honouring that literally would render add and del as
	// the same band, so a diff would show that something changed but not
	// which way. Fall back to conventional green/red, tuned to the page.
	if !distinctAccents(addAccent, delAccent) {
		addAccent, delAccent = fallbackDiffAccents(dark)
	}
	p = palette{
		fg:     fg,
		muted:  tok(chroma.Comment, dimFg),
		accent: tok(chroma.Keyword, fg),
		user:   tok(chroma.NameFunction, tok(chroma.GenericInserted, fg)),
		userBg: mixHex(background, fg, 0.07), // block just off the terminal bg
		err:    tok(chroma.GenericDeleted, tok(chroma.Error, fg)),
		warn:   tok(chroma.NameDecorator, tok(chroma.LiteralString, fg)),
		border: borderForChrome(background, fg),
		add:    addAccent,
		del:    delAccent,
		meta:   tok(chroma.LiteralNumber, tok(chroma.KeywordType, fg)),
		slash:  tok(chroma.LiteralString, tok(chroma.NameDecorator, fg)),
	}
	return p, dark, true
}

// diffAccentFrom extracts a usable diff accent from a chroma style entry.
//
// Styles disagree about which channel carries the signal. monokai and nord set
// the foreground of an inserted line and leave its background at the page
// colour; gruvbox does the reverse, painting a green background with text in
// the page colour. Taking .Colour unconditionally makes gruvbox report the
// page background for both add and del, so the rows render identical and
// invisible — a diff with no visible sign of what changed.
//
// Prefer the channel that actually differs from the page background, fall back
// to the other, then to the caller's token fallback.
func diffAccentFrom(s *chroma.Style, tt chroma.TokenType, background, fallback string) string {
	e := s.Get(tt)
	fgSet, bgSet := e.Colour.IsSet(), e.Background.IsSet()
	fgc, bgc := e.Colour.String(), e.Background.String()

	// A channel is informative only when it is distinguishable from the page.
	informative := func(set bool, hex string) bool {
		return set && contrastRatio(hex, background) >= minChromaAccentContrast
	}
	if informative(fgSet, fgc) {
		return fgc
	}
	if informative(bgSet, bgc) {
		return bgc
	}
	// Neither channel says anything useful; let the caller's fallback decide
	// rather than returning a colour equal to the surface.
	return fallback
}

// distinctAccents reports whether two diff accents are far enough apart to
// tell an insertion from a deletion at a glance.
//
// The metric is deliberately not WCAG contrast. Contrast is a luminance ratio
// and is blind to hue: github-dark's green #56d364 and salmon #ffa198 score
// 1.009 against each other — indistinguishable by that measure, obviously
// different to a reader. Judging diff colours by contrast would throw away
// perfectly good palettes. Channel distance is crude but it sees hue, which is
// the thing that actually separates "added" from "removed".
func distinctAccents(add, del string) bool {
	if strings.TrimSpace(add) == "" || strings.TrimSpace(del) == "" {
		return false
	}
	return colorDistance(add, del) >= minDiffAccentSeparation
}

// colorDistance is Euclidean distance in RGB (0 = identical, ~441 = black to
// white). Not perceptually uniform, but it is monotonic in the way that
// matters here and needs no colour-space conversion.
func colorDistance(a, b string) float64 {
	ar, ag, ab := parseHexRGB(a)
	br, bg, bb := parseHexRGB(b)
	dr := float64(ar - br)
	dg := float64(ag - bg)
	db := float64(ab - bb)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// fallbackDiffAccents is the green/red pair used when a style declines to
// distinguish insertions from deletions.
//
// Convention wins here over palette coherence. A user who picks a theme with
// no diff colours still expects green to mean added and red to mean removed;
// inventing two arbitrary hues from the style would be prettier and less
// readable. These match the shipped default palette so the fallback looks
// deliberate rather than bolted on.
func fallbackDiffAccents(dark bool) (add, del string) {
	if dark {
		return "#4ADE80", "#F87171"
	}
	return "#047857", "#B91C1C"
}

// parseHexRGB parses #rgb / #rrggbb into 0-255 components (0,0,0 on garbage).
// Chroma's Colour.String can emit negative digits for malformed entries
// ("#-00001" for github's Background colour); negatives clamp to 0 rather
// than failing the whole parse (which silently read every such color as
// black and broke mixes/contrast math).
func parseHexRGB(s string) (int, int, int) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return 0, 0, 0
	}
	// Parse per byte so a negative component ("#-00001") clamps to 0 without
	// corrupting the neighboring channels via two's-complement bit patterns.
	clamp := func(x int64, err error) int {
		if err != nil || x < 0 {
			return 0
		}
		return min(255, int(x))
	}
	r, errR := strconv.ParseInt(s[0:2], 16, 32)
	g, errG := strconv.ParseInt(s[2:4], 16, 32)
	b, errB := strconv.ParseInt(s[4:6], 16, 32)
	return clamp(r, errR), clamp(g, errG), clamp(b, errB)
}

// mixHex linearly blends a→b (t in [0,1]) and returns #rrggbb.
func mixHex(a, b string, t float64) string {
	ar, ag, ab := parseHexRGB(a)
	br, bg, bb := parseHexRGB(b)
	mix := func(x, y int) int {
		v := int(float64(x)*(1-t) + float64(y)*t + 0.5)
		return max(0, min(255, v))
	}
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb))
}

// borderForChrome mixes the chrome border token for chroma-derived themes,
// but never lets it fuse with the terminal background: a 0.20 wash lands
// within a few points of bg on light styles (github: #f3f4f5 on a white pane)
// and renders rules/boxes invisible. 0.32 keeps it subtle yet visible on both
// light and dark surfaces.
func borderForChrome(background, fg string) string {
	border := mixHex(background, fg, 0.20)
	if contrastRatio(border, background) < 1.30 {
		border = mixHex(background, fg, 0.32)
	}
	return border
}

// contrastRatio is WCAG relative-luminance contrast (1 = identical colors).
func contrastRatio(a, b string) float64 {
	l1, l2 := hexLuminance(a), hexLuminance(b)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// hexLuminance is WCAG relative luminance in [0,1] (linearized channels).
func hexLuminance(s string) float64 {
	r, g, b := parseHexRGB(s)
	lin := func(v int) float64 {
		c := float64(v) / 255.0
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// hexBrightness is perceptual luminance in [0,1].
func hexBrightness(s string) float64 {
	r, g, b := parseHexRGB(s)
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 255.0
}

// newTheme builds the default adaptive-style palette from terminal dark/light.
func newTheme() theme {
	return newThemeFrom(ThemeConfig{}, darkTheme())
}

// DefaultThemeName is the theme applied when the user configures none. Shipped
// as catppuccin-mocha; the built-in adaptive palette stays available via
// `name: default`.
const DefaultThemeName = "catppuccin-mocha"

// newThemeFrom builds a theme from config + pinned terminal dark/light.
//
// theme.name accepts the curated preset (default) OR any chroma style
// name (catppuccin-mocha, dracula, nord, …): a chroma name derives the whole
// palette from that style and defaults code highlighting to the same style, so
// one name themes the entire UI. theme.code overrides code highlighting only.
// Empty (unconfigured) resolves to DefaultThemeName.
func newThemeFrom(cfg ThemeConfig, termDark bool) theme {
	raw := strings.ToLower(strings.TrimSpace(cfg.Name))
	if raw == "" {
		raw = DefaultThemeName
	}
	var p palette
	name := raw
	mdDark := termDark
	codeDefault := "" // named chroma style code falls back to (empty = palette-derived)
	switch raw {
	case "default":
		name = "default"
		p = defaultPalette(termDark)
	default:
		if cp, dark, ok := paletteFromChroma(raw); ok {
			p = cp
			mdDark = dark
			// Code stays palette-derived (quiet). Opt into full chroma noise with
			// theme.code: catppuccin-mocha (etc.) when you want splash fidelity.
			codeDefault = ""
		} else {
			slog.Warn("mowi: theme.name unknown (want default, or a chroma style like catppuccin-mocha, dracula, nord)", "value", cfg.Name)
			name = "default"
			p = defaultPalette(termDark)
		}
	}
	p = applyColorOverrides(p, cfg.Colors)
	// Code highlighting: explicit theme.code wins; else the chroma-derived
	// default; else palette-derived so the frame and markdown share one theme.
	chroma := resolveChromaStyle(cfg.Code)
	if chroma == "" {
		chroma = codeDefault
	}
	if noColor() {
		// Honor NO_COLOR (no-color.org): blank every palette entry so chrome
		// (c(hex)) and glamour (styleStr(hex)) both emit no color. A named
		// chroma style would otherwise still paint code, so drop it too.
		p = palette{}
		chroma = ""
	}
	return buildTheme(name, p, mdDark, chroma)
}

// noColor reports the NO_COLOR convention: set to any non-empty value disables
// color. MOW_FORCE_COLOR overrides (for piping into a color-capable pager).
func noColor() bool {
	if v := strings.TrimSpace(os.Getenv("MOW_FORCE_COLOR")); v != "" && v != "0" {
		return false
	}
	return strings.TrimSpace(os.Getenv("NO_COLOR")) != ""
}

// isHexColor accepts #RGB and #RRGGBB.
func isHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func applyColorOverrides(p palette, colors map[string]string) palette {
	if len(colors) == 0 {
		return p
	}
	get := func(k string) string {
		v := strings.TrimSpace(colors[k])
		if v == "" {
			return ""
		}
		if !strings.HasPrefix(v, "#") {
			v = "#" + v
		}
		// Validate: "accent: red" used to become the broken color "#red"
		// and silently paint nothing.
		if !isHexColor(v) {
			slog.Warn("mowi: theme color ignored (want hex like #FFD866)", "key", k, "value", colors[k])
			return ""
		}
		return v
	}
	if v := get("fg"); v != "" {
		p.fg = v
	}
	if v := get("muted"); v != "" {
		p.muted = v
	}
	if v := get("accent"); v != "" {
		p.accent = v
	}
	if v := get("user"); v != "" {
		p.user = v
	}
	if v := get("user_bg"); v != "" {
		p.userBg = v
	}
	if v := get("error"); v != "" {
		p.err = v
	}
	if v := get("warn"); v != "" {
		p.warn = v
	}
	if v := get("border"); v != "" {
		p.border = v
	}
	if v := get("diff_add"); v != "" {
		p.add = v
	}
	if v := get("diff_del"); v != "" {
		p.del = v
	}
	if v := get("diff_meta"); v != "" {
		p.meta = v
	}
	if v := get("diff_add_bg"); v != "" {
		p.addBg = v
	}
	if v := get("diff_del_bg"); v != "" {
		p.delBg = v
	}
	if v := get("slash"); v != "" {
		p.slash = v
	}
	return p
}

func buildTheme(name string, p palette, mdDark bool, chroma string) theme {
	// Fixed Color — no AdaptiveColor / OSC probes.
	c := func(hex string) color.Color { return lipgloss.Color(hex) }
	square := lipgloss.NormalBorder()
	return theme{
		name:        name,
		palette:     p,
		mdDark:      mdDark,
		chromaStyle: chroma,
		Accent:      lipgloss.NewStyle().Foreground(c(p.accent)).Bold(true),
		Muted:       lipgloss.NewStyle().Foreground(c(p.muted)),
		Error:       lipgloss.NewStyle().Foreground(c(p.err)),
		Warn:        lipgloss.NewStyle().Foreground(c(p.warn)),
		Header: lipgloss.NewStyle().
			Foreground(c(p.fg)).
			Bold(true).
			Padding(0, 1),
		Text: lipgloss.NewStyle().Foreground(c(p.fg)),
		Status: lipgloss.NewStyle().
			Foreground(c(p.muted)).
			Padding(0, 1),
		Input:    lipgloss.NewStyle().Padding(0, 1),
		InputFoc: lipgloss.NewStyle().Padding(0, 1),
		Box: lipgloss.NewStyle().
			Border(square).
			BorderForeground(c(p.border)).
			Padding(0, 1),
		Title:       lipgloss.NewStyle().Bold(true).Foreground(c(p.accent)),
		RoleUserBar: lipgloss.NewStyle().Foreground(c(p.user)),
		RoleAsstBar: lipgloss.NewStyle().Foreground(c(p.accent)),
		RoleUserBg:  lipgloss.NewStyle().Background(c(p.userBg)).Foreground(c(p.fg)),
		StampUser:   lipgloss.NewStyle().Background(c(p.userBg)).Foreground(c(p.muted)),
		DiffAdd:     diffAddStyle(c, p, mdDark),
		DiffDel:     diffDelStyle(c, p, mdDark),
		DiffMeta:    lipgloss.NewStyle().Foreground(c(p.meta)),
		DiffNum:     lipgloss.NewStyle().Foreground(c(p.muted)).Faint(true),
		DiffCtx:     lipgloss.NewStyle().Foreground(c(p.fg)),
		Sep:         lipgloss.NewStyle().Foreground(c(p.border)),
		SlashCmd:    lipgloss.NewStyle().Foreground(c(p.slash)).Bold(true),
	}
}

// Soft row backgrounds from the theme palette (not fixed green/red hex).
func diffAddStyle(c func(string) color.Color, p palette, dark bool) lipgloss.Style {
	st := lipgloss.NewStyle()
	bg := resolveDiffBg(p.addBg, p.add, p, dark)
	if p.add != "" {
		st = st.Foreground(c(diffFgOn(p.add, bg, dark)))
	}
	if bg != "" {
		st = st.Background(c(bg))
	}
	return st
}

func diffDelStyle(c func(string) color.Color, p palette, dark bool) lipgloss.Style {
	st := lipgloss.NewStyle()
	bg := resolveDiffBg(p.delBg, p.del, p, dark)
	if p.del != "" {
		st = st.Foreground(c(diffFgOn(p.del, bg, dark)))
	}
	if bg != "" {
		st = st.Background(c(bg))
	}
	return st
}

// Diff row banding thresholds, in WCAG contrast ratio (1.0 = identical).
//
// These are the numbers that decide whether a diff reads as a band or as
// plain text. They are deliberately expressed as contrast against the
// surrounding surface rather than as a mix ratio, because a fixed ratio
// produces wildly different results per theme.
const (
	// minDiffBandContrast is how far the row background must sit from the
	// surface behind it. Below roughly 1.5 the band stops registering as a
	// block on most displays, especially light themes.
	minDiffBandContrast = 1.75
	// minDiffTextContrast is the floor for the row's own text against its new
	// background. The band must never be strengthened to the point where the
	// +/− line itself becomes hard to read.
	minDiffTextContrast = 2.6
	// minDiffNumContrast is the floor for gutter line numbers on a band. It is
	// the strictest of the three (WCAG AA for normal text): numbers are small,
	// dense, and the thing you navigate by, so "technically visible" is not
	// enough.
	minDiffNumContrast = 4.5
	// diffNumHeadroom is aimed for above the hard floor so a later palette or
	// band tweak degrades toward the minimum instead of straight through it.
	diffNumHeadroom = 0.35
	// minChromaAccentContrast decides whether a chroma style's diff colour
	// carries information. A value this close to the page background is the
	// style saying "no colour here" (gruvbox paints inserted text in the page
	// colour and puts the signal on the background instead), not a deliberate
	// near-invisible accent.
	minChromaAccentContrast = 1.20
	// minDiffAccentSeparation is how far apart the add and del accents must
	// sit, as RGB channel distance (0 = identical, ~441 = black to white).
	// Styles that fail this are ones with literally equal diff colours, not
	// merely close ones — 40 catches those without rejecting palettes whose
	// green and red are subtle.
	minDiffAccentSeparation = 40.0
	// minDiffBandSeparation is the same idea for the derived backgrounds.
	// Bands are washes, so they converge toward the surface and sit closer
	// together than the accents they came from; holding them to the accent
	// threshold would reject palettes that render perfectly well. The band
	// only has to avoid reading as one block — direction is carried by the
	// tinted line numbers and the +/− glyph, not by the wash alone.
	minDiffBandSeparation = 15.0
)

// resolveDiffBg prefers an explicit override; otherwise washes accent into the
// theme surface (user_bg / border) so chroma-derived and custom stay coherent.
//
// A fixed mix ratio is not enough on its own. How far a wash actually travels
// depends on how far apart the accent and the surface already are, so the same
// t lands very differently per theme: on the default light palette a 0.24 wash
// of green into #E9E9EC produced only 1.38 contrast against the surface —
// technically a tint, visually nothing. The ratio is therefore a starting
// point, and the loop below pushes further until the row reads as a band.
func resolveDiffBg(override, accent string, p palette, dark bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if strings.TrimSpace(accent) == "" {
		return ""
	}
	base := p.userBg
	if base == "" {
		base = p.border
	}
	if base == "" {
		if dark {
			base = "#1e1e2e"
		} else {
			base = "#f3f4f6"
		}
	}
	// The band backs the content only: line numbers and the change glyph are
	// tinted, not washed. Nothing legible sits on this colour any more, which
	// removes the ceiling an earlier version had to respect (numbers needed
	// 4.5:1 against the band, capping how dark it could go) and lets the wash
	// be a quiet backing rather than a compromise between two jobs. Deeper and
	// duller reads better behind syntax-coloured code.
	t := 0.30
	if !dark {
		t = 0.26
	}
	bg := mixHex(base, accent, t)
	// Guarantee the band is actually visible against the surface it sits on,
	// whatever the theme's colors happen to be. Without this floor a palette
	// whose accent sits close to its surface (light themes, low-contrast
	// chroma styles) renders diff rows as plain text.
	for i := 0; i < 8 && contrastRatio(bg, base) < minDiffBandContrast; i++ {
		t += 0.08
		if t > 0.92 {
			break
		}
		bg = mixHex(base, accent, t)
	}
	// When the accent is itself close to the surface, mixing the two can never
	// escape the surface — a fully saturated wash still lands on top of it.
	// Custom themes do hit this. Step the band toward the opposite luminance
	// pole instead so the row separates even from a palette that gives us
	// nothing to work with; the hue is already lost in that case, so keeping
	// the row *findable* matters more than keeping it tinted.
	if contrastRatio(bg, base) < minDiffBandContrast {
		pole := "#ffffff"
		if !dark {
			pole = "#000000"
		}
		for i := 0; i < 10 && contrastRatio(bg, base) < minDiffBandContrast; i++ {
			bg = mixHex(bg, pole, 0.10)
		}
	}
	return bg
}

// diffFgOn returns a readable foreground for a diff row painted on bg.
//
// The band strength and the text legibility used to fight each other: pushing
// the wash far enough to read as a block left the accent-colored text sitting
// on a near-identical background, and backing the wash off to fix that undid
// the banding. Adapting the *text* instead settles it — the row keeps its
// full-strength band, and the glyphs move toward the surface's light or dark
// pole until they read cleanly.
func diffFgOn(accent, bg string, dark bool) string {
	if strings.TrimSpace(accent) == "" || strings.TrimSpace(bg) == "" {
		return accent
	}
	if contrastRatio(accent, bg) >= minDiffTextContrast {
		return accent
	}
	// Move away from the background: lighten on dark themes, darken on light.
	pole := "#ffffff"
	if !dark {
		pole = "#000000"
	}
	fg := accent
	for i := 0; i < 8; i++ {
		fg = mixHex(fg, pole, 0.18)
		if contrastRatio(fg, bg) >= minDiffTextContrast {
			break
		}
	}
	return fg
}

// chrome (accent, muted, fg, …) so transcript markdown does not look like a
// different app inside the TUI.
