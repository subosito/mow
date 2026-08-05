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
	name   string // resolved name: default | monokai
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
	// DiffNumOnBand is the line-number ink once a row carries an add/del wash.
	// The muted+dim gutter tone measures ~1.7:1 against those backgrounds, so
	// numbers vanish exactly where you navigate by them; this is derived to
	// clear AA on both bands while staying below body-text weight.
	DiffNumOnBand color.Color
	Sep           lipgloss.Style
	// SlashCmd colors /commands in the input field.
	SlashCmd lipgloss.Style
	// palette is the hex token set shared by chrome and glamour markdown.
	palette palette
	// mdDark: dark document base (monokai always dark).
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
)

// palette is fixed hex colors (no AdaptiveColor probes).
type palette struct {
	fg, muted, accent, user, userBg          string
	err, warn, border, add, del, meta, slash string
	// Optional soft row washes for review-style diffs. Empty → derived from
	// add/del mixed into userBg so every theme (chroma name, monokai, custom
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

// monokaiProPalette is Monokai Pro (Filter Spectrum / classic dark).
// Source: monokai.pro contribute swatches.
//
//	bg #2D2A2E  fg #FCFCFA  dim #727072
//	red #FF6188  orange #FC9867  yellow #FFD866
//	green #A9DC76  cyan #78DCE8  purple #AB9DF2
func monokaiProPalette() palette {
	return palette{
		fg:     "#FCFCFA",
		muted:  "#727072",
		accent: "#AB9DF2", // purple — chrome / assistant gutter
		user:   "#A9DC76", // green — user gutter
		userBg: "#221F22", // slightly darker than #2D2A2E
		err:    "#FF6188",
		warn:   "#FFD866",
		border: "#5B595C",
		add:    "#A9DC76",
		del:    "#FF6188",
		meta:   "#78DCE8",
		slash:  "#FC9867", // orange for /commands
	}
}

// ThemeConfig is extensions.tui.theme (YAML). One theme drives both the frame
// (chrome) and the markdown renderer — the palette is the single source of
// truth, so a full custom theme is just `colors` with every token set.
//
//	theme:
//	  name: catppuccin-mocha  # default | monokai | any chroma style name
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

// NormalizeThemeName returns "default" or "monokai".
func NormalizeThemeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "monokai" {
		return "monokai"
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
	p = palette{
		fg:     fg,
		muted:  tok(chroma.Comment, dimFg),
		accent: tok(chroma.Keyword, fg),
		user:   tok(chroma.NameFunction, tok(chroma.GenericInserted, fg)),
		userBg: mixHex(background, fg, 0.07), // block just off the terminal bg
		err:    tok(chroma.GenericDeleted, tok(chroma.Error, fg)),
		warn:   tok(chroma.NameDecorator, tok(chroma.LiteralString, fg)),
		border: borderForChrome(background, fg),
		add:    tok(chroma.GenericInserted, tok(chroma.NameFunction, fg)),
		del:    tok(chroma.GenericDeleted, tok(chroma.Error, fg)),
		meta:   tok(chroma.LiteralNumber, tok(chroma.KeywordType, fg)),
		slash:  tok(chroma.LiteralString, tok(chroma.NameDecorator, fg)),
	}
	return p, dark, true
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
// theme.name accepts a curated preset (default | monokai) OR any chroma style
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
	case "monokai":
		name = "monokai"
		p = monokaiProPalette()
		mdDark = true
	default:
		if cp, dark, ok := paletteFromChroma(raw); ok {
			p = cp
			mdDark = dark
			// Code stays palette-derived (quiet). Opt into full chroma noise with
			// theme.code: catppuccin-mocha (etc.) when you want splash fidelity.
			codeDefault = ""
		} else {
			slog.Warn("mowi: theme.name unknown (want default, monokai, or a chroma style like catppuccin-mocha)", "value", cfg.Name)
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
		Title:         lipgloss.NewStyle().Bold(true).Foreground(c(p.accent)),
		RoleUserBar:   lipgloss.NewStyle().Foreground(c(p.user)),
		RoleAsstBar:   lipgloss.NewStyle().Foreground(c(p.accent)),
		RoleUserBg:    lipgloss.NewStyle().Background(c(p.userBg)).Foreground(c(p.fg)),
		StampUser:     lipgloss.NewStyle().Background(c(p.userBg)).Foreground(c(p.muted)),
		DiffAdd:       diffAddStyle(c, p, mdDark),
		DiffDel:       diffDelStyle(c, p, mdDark),
		DiffMeta:      lipgloss.NewStyle().Foreground(c(p.meta)),
		DiffNum:       lipgloss.NewStyle().Foreground(c(p.muted)).Faint(true),
		DiffNumOnBand: c(mixHex(p.muted, p.fg, 0.8)),
		DiffCtx:       lipgloss.NewStyle().Foreground(c(p.fg)),
		Sep:           lipgloss.NewStyle().Foreground(c(p.border)),
		SlashCmd:      lipgloss.NewStyle().Foreground(c(p.slash)).Bold(true),
	}
}

// Soft row backgrounds from the theme palette (not fixed green/red hex).
func diffAddStyle(c func(string) color.Color, p palette, dark bool) lipgloss.Style {
	st := lipgloss.NewStyle()
	if p.add != "" {
		st = st.Foreground(c(p.add))
	}
	if bg := resolveDiffBg(p.addBg, p.add, p, dark); bg != "" {
		st = st.Background(c(bg))
	}
	return st
}

func diffDelStyle(c func(string) color.Color, p palette, dark bool) lipgloss.Style {
	st := lipgloss.NewStyle()
	if p.del != "" {
		st = st.Foreground(c(p.del))
	}
	if bg := resolveDiffBg(p.delBg, p.del, p, dark); bg != "" {
		st = st.Background(c(bg))
	}
	return st
}

// resolveDiffBg prefers an explicit override; otherwise washes accent into the
// theme surface (user_bg / border) so monokai/catppuccin/custom stay coherent.
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
	// Dark themes need a stronger wash to stay visible on near-black surfaces.
	t := 0.35
	if !dark {
		t = 0.24
	}
	return mixHex(base, accent, t)
}

// markdownStyle is the glamour StyleConfig for this theme — same hex tokens as
// chrome (accent, muted, fg, …) so transcript markdown does not look like a
// different app inside the TUI.
