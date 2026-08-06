package mowi

import (
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestNormalizeThemeName(t *testing.T) {
	// "default" is the sole curated preset; every other accepted name is a
	// chroma style, resolved by the generic path.
	cases := map[string]string{
		"":            "default",
		"  ":          "default",
		"DEFAULT":     "default",
		"Default":     "default",
		"nope-theme":  "default", // unknown → default
		"monokai-pro": "default", // not a chroma style
		// monokai is no longer a hand-written preset, but chroma ships one,
		// so an existing config keeps working through the generic path.
		"Monokai":   "monokai",
		"monokai":   "monokai",
		" MONOKAI ": "monokai",
		// Other chroma styles resolve to themselves.
		"dracula": "dracula",
		"nord":    "nord",
	}
	for in, want := range cases {
		if got := NormalizeThemeName(in); got != want {
			t.Errorf("NormalizeThemeName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsHexColor(t *testing.T) {
	ok := []string{"#fff", "#FFF", "#aabbcc", "#AAbbCC", "#000", "#ffffff"}
	bad := []string{"", "#ff", "#ffff", "#gg0000", "fff", " #aabbcc", "#aabbcc ", "red", "12", "#12345", "#1234567"}
	for _, s := range ok {
		if !isHexColor(s) {
			t.Errorf("isHexColor(%q) = false", s)
		}
	}
	for _, s := range bad {
		if isHexColor(s) {
			t.Errorf("isHexColor(%q) = true", s)
		}
	}
}

func TestNamedThemePalettes(t *testing.T) {
	// The curated preset plus a chroma style that used to be a second preset.
	for _, name := range []string{"default", "monokai"} {
		th := newThemeFrom(ThemeConfig{Name: name}, true)
		p := th.palette
		if p.fg == "" || p.accent == "" || p.muted == "" || p.userBg == "" {
			t.Fatalf("%s: incomplete palette %+v", name, p)
		}
		if th.Header.GetForeground() == nil {
			t.Fatalf("%s: Header has no fg", name)
		}
		if th.Accent.GetForeground() == nil {
			t.Fatalf("%s: Accent has no fg", name)
		}
		if th.RoleUserBg.GetBackground() == nil {
			t.Fatalf("%s: RoleUserBg has no bg", name)
		}
		if th.name != NormalizeThemeName(name) {
			t.Fatalf("%s: name=%q", name, th.name)
		}
	}

	// monokai forces mdDark; code highlighting derives from the palette by
	// default (no named chroma) so the frame and markdown stay one theme.
	mono := newThemeFrom(ThemeConfig{Name: "monokai"}, false)
	if !mono.mdDark {
		t.Fatal("monokai must force mdDark even on light terminal")
	}
	if mono.chromaStyle != "" {
		t.Fatalf("monokai chromaStyle=%q want empty (palette-derived)", mono.chromaStyle)
	}
	// Opt into a named chroma style for code via theme.code.
	monoCode := newThemeFrom(ThemeConfig{Name: "monokai", Code: "monokai"}, false)
	if monoCode.chromaStyle != "monokai" {
		t.Fatalf("theme.code=monokai chromaStyle=%q want monokai", monoCode.chromaStyle)
	}
	// Unknown chroma style is ignored (falls back to palette-derived). Use a
	// palette-based base (monokai) so the fallback is "" rather than the
	// chroma-derived default of a chroma-named theme.
	bad := newThemeFrom(ThemeConfig{Name: "monokai", Code: "definitely-not-a-style"}, true)
	if bad.chromaStyle != "" {
		t.Fatalf("unknown theme.code chromaStyle=%q want empty", bad.chromaStyle)
	}

	// Unknown name → default palette (dark).
	unk := newThemeFrom(ThemeConfig{Name: "not-a-real-theme"}, true)
	def := newThemeFrom(ThemeConfig{Name: "default"}, true)
	if unk.palette != def.palette {
		t.Fatalf("unknown theme palette=%+v want default %+v", unk.palette, def.palette)
	}
	if unk.name != "default" {
		t.Fatalf("unknown name=%q want default", unk.name)
	}
}

func TestMonokaiIgnoresLightTerminal(t *testing.T) {
	dark := newThemeFrom(ThemeConfig{Name: "monokai"}, true)
	light := newThemeFrom(ThemeConfig{Name: "monokai"}, false)
	if dark.palette != light.palette {
		t.Fatalf("monokai forceDark failed dark=%+v light=%+v", dark.palette, light.palette)
	}
	if !dark.mdDark || !light.mdDark {
		t.Fatal("monokai mdDark must stay true")
	}

	// default adapts to the terminal.
	defDark := newThemeFrom(ThemeConfig{Name: "default"}, true)
	defLight := newThemeFrom(ThemeConfig{Name: "default"}, false)
	if defDark.palette.fg == defLight.palette.fg && defDark.palette.userBg == defLight.palette.userBg {
		t.Fatal("default theme should differ between light and dark terminals")
	}
	if defLight.mdDark {
		t.Fatal("default on light terminal should have mdDark=false")
	}
	if !defDark.mdDark {
		t.Fatal("default on dark terminal should have mdDark=true")
	}
	// default has no named chroma (palette-derived).
	if defDark.chromaStyle != "" {
		t.Fatalf("default chromaStyle=%q want empty", defDark.chromaStyle)
	}
}

func TestThemeColorOverrides(t *testing.T) {
	// Force color so style renders emit truecolor SGR we can assert on.
	t.Setenv("NO_COLOR", "")
	t.Setenv("MOW_FORCE_COLOR", "1")

	cfg := ThemeConfig{
		Name: "default",
		Colors: map[string]string{
			"fg":        "#112233",
			"muted":     "#666666",
			"accent":    "#ff00aa",
			"user":      "#00ffaa",
			"user_bg":   "#223344",
			"error":     "#ff0000",
			"warn":      "#ffaa00",
			"border":    "#445566",
			"diff_add":  "#00ff00",
			"diff_del":  "#ff00ff",
			"diff_meta": "#0000ff",
			"slash":     "#ffff00",
		},
	}
	th := newThemeFrom(cfg, true)
	p := th.palette
	checks := map[string]string{
		"fg": p.fg, "muted": p.muted, "accent": p.accent, "user": p.user,
		"userBg": p.userBg, "err": p.err, "warn": p.warn, "border": p.border,
		"add": p.add, "del": p.del, "meta": p.meta, "slash": p.slash,
	}
	want := map[string]string{
		"fg": "#112233", "muted": "#666666", "accent": "#ff00aa", "user": "#00ffaa",
		"userBg": "#223344", "err": "#ff0000", "warn": "#ffaa00", "border": "#445566",
		"add": "#00ff00", "del": "#ff00ff", "meta": "#0000ff", "slash": "#ffff00",
	}
	for k, got := range checks {
		if !strings.EqualFold(got, want[k]) {
			t.Errorf("%s=%q want %q", k, got, want[k])
		}
	}

	// Styles pick up overridden colors — assert via truecolor SGR in Render.
	assertRGB(t, th.Accent.Render("x"), 255, 0, 170)    // #ff00aa
	assertRGB(t, th.Error.Render("x"), 255, 0, 0)       // #ff0000
	assertRGB(t, th.SlashCmd.Render("x"), 255, 255, 0)  // #ffff00
	assertRGB(t, th.DiffAdd.Render("x"), 0, 255, 0)     // #00ff00
	assertRGB(t, th.RoleUserBg.Render("x"), 34, 51, 68) // #223344 bg
}

func TestThemeOverrideWithoutHash(t *testing.T) {
	// "FFD866" should be accepted as "#FFD866".
	th := newThemeFrom(ThemeConfig{
		Name:   "default",
		Colors: map[string]string{"accent": "FFD866"},
	}, true)
	if !strings.EqualFold(th.palette.accent, "#FFD866") {
		t.Fatalf("accent=%q want #FFD866", th.palette.accent)
	}
}

func TestThemeOverrideInvalidHexIgnored(t *testing.T) {
	base := newThemeFrom(ThemeConfig{Name: "monokai"}, true)
	th := newThemeFrom(ThemeConfig{
		Name: "monokai",
		Colors: map[string]string{
			"fg":     "",        // no-op
			"accent": "#gg0000", // invalid
			"muted":  "red",     // not hex
			"user":   "#fff",    // valid short hex — applied
		},
	}, true)
	if th.palette.fg != base.palette.fg {
		t.Fatalf("empty FG changed palette fg: got %q want %q", th.palette.fg, base.palette.fg)
	}
	if th.palette.accent != base.palette.accent {
		t.Fatalf("invalid accent changed palette: got %q want %q", th.palette.accent, base.palette.accent)
	}
	if th.palette.muted != base.palette.muted {
		t.Fatalf("named color 'red' must be ignored: got %q", th.palette.muted)
	}
	if !strings.EqualFold(th.palette.user, "#fff") {
		t.Fatalf("valid short hex user=%q want #fff", th.palette.user)
	}
}

func TestNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("MOW_FORCE_COLOR", "")
	if noColor() {
		t.Fatal("color should be on by default (noColor=false)")
	}

	t.Setenv("NO_COLOR", "1")
	if !noColor() {
		t.Fatal("NO_COLOR must disable color")
	}

	t.Setenv("MOW_FORCE_COLOR", "1")
	if noColor() {
		t.Fatal("MOW_FORCE_COLOR overrides NO_COLOR")
	}

	t.Setenv("MOW_FORCE_COLOR", "0")
	t.Setenv("NO_COLOR", "1")
	// force=0 does not override; NO_COLOR still wins.
	if !noColor() {
		t.Fatal("MOW_FORCE_COLOR=0 should not override NO_COLOR")
	}

	t.Setenv("MOW_FORCE_COLOR", "0")
	t.Setenv("NO_COLOR", "")
	if noColor() {
		t.Fatal("no NO_COLOR + FORCE=0 → color on")
	}
}

func TestNoColorBlanksPalette(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("MOW_FORCE_COLOR", "")
	th := newThemeFrom(ThemeConfig{Name: "monokai"}, true)
	p := th.palette
	if p.fg != "" || p.accent != "" || p.userBg != "" || p.err != "" {
		t.Fatalf("NO_COLOR should blank palette, got %+v", p)
	}
}

func TestDetectDarkColorFGBG(t *testing.T) {
	// colorFGBGBackground parses the last ;-separated index.
	t.Setenv("COLORFGBG", "15;0") // light fg, dark bg (0)
	if bg := colorFGBGBackground(); bg != 0 {
		t.Fatalf("bg index=%d want 0", bg)
	}
	if !detectDarkBackground() {
		t.Fatal("COLORFGBG 15;0 should be dark")
	}

	t.Setenv("COLORFGBG", "0;15") // dark fg, light bg (15)
	if detectDarkBackground() {
		t.Fatal("COLORFGBG 0;15 should be light")
	}

	t.Setenv("COLORFGBG", "0;7") // bg=7 white → light
	if detectDarkBackground() {
		t.Fatal("COLORFGBG 0;7 should be light")
	}

	t.Setenv("COLORFGBG", "7;8") // bg=8 → dark
	if !detectDarkBackground() {
		t.Fatal("COLORFGBG 7;8 should be dark")
	}

	t.Setenv("COLORFGBG", "")
	if colorFGBGBackground() != -1 {
		t.Fatal("empty COLORFGBG should return -1")
	}
	t.Setenv("COLORFGBG", "nope")
	if colorFGBGBackground() != -1 {
		t.Fatal("malformed COLORFGBG should return -1")
	}
	t.Setenv("COLORFGBG", "1;x")
	if colorFGBGBackground() != -1 {
		t.Fatal("non-int bg should return -1")
	}
}

func TestPinTerminalThemeStable(t *testing.T) {
	// pinTerminalTheme is sync.Once — stable across calls in-process.
	t.Setenv("COLORFGBG", "15;0")
	a := pinTerminalTheme()
	b := pinTerminalTheme()
	if a != b {
		t.Fatalf("pinTerminalTheme unstable: %v then %v", a, b)
	}
	// darkTheme mirrors the pin.
	if darkTheme() != a {
		t.Fatalf("darkTheme=%v pin=%v", darkTheme(), a)
	}
}

func TestNewThemeDefaultHelper(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	if th.palette.fg == "" {
		t.Fatal("newTheme returned empty palette without NO_COLOR")
	}
	if th.Accent.GetForeground() == nil {
		t.Fatal("newTheme Accent missing fg")
	}
	// Unconfigured resolves to the shipped default theme (catppuccin-mocha).
	if th.name != DefaultThemeName {
		t.Fatalf("name=%q want %q", th.name, DefaultThemeName)
	}
}

func TestMarkdownStyleTracksPalette(t *testing.T) {
	th := newThemeFrom(ThemeConfig{
		Name: "monokai",
		Colors: map[string]string{
			"fg":        "#f8f8f2",
			"accent":    "#ff6188",
			"diff_meta": "#78dce8",
			"slash":     "#ffd866",
			"user_bg":   "#221f22",
			"muted":     "#727072",
			"border":    "#5b595c",
		},
	}, true)
	st := mdStyleFromPalette(th.palette, th.mdDark, th.chromaStyle)
	if st.fg != "#f8f8f2" {
		t.Fatalf("fg=%q", st.fg)
	}
	if st.accent != "#ff6188" {
		t.Fatalf("accent=%q", st.accent)
	}
	if st.meta != "#78dce8" {
		t.Fatalf("meta=%q", st.meta)
	}
	if st.userBg != "#221f22" {
		t.Fatalf("userBg=%q", st.userBg)
	}
	// Code blocks derive from the palette too, so overrides reach fenced code
	// (no named chroma unless theme.code opts in).
	if st.chromaName != "" {
		t.Fatalf("chromaName=%q want empty (palette-derived)", st.chromaName)
	}
	// No h1 background block (viewport-safe) — headings are accent text only.
	md := &model{theme: th, md: newMDCacheFromTheme(th)}
	hout := md.renderMarkdown("# Hello", 60, false)
	if !strings.Contains(hout, "Hello") || strings.Contains(hout, "\x1b[48") {
		t.Fatalf("h1 painted a background block: %q", hout)
	}
}

func TestMarkdownStyleDefaultUsesPaletteChroma(t *testing.T) {
	th := newThemeFrom(ThemeConfig{Name: "default"}, true)
	st := mdStyleFromPalette(th.palette, th.mdDark, th.chromaStyle)
	if st.chromaName != "" {
		t.Fatalf("default chromaName=%q want empty (palette chroma)", st.chromaName)
	}
	// Fenced code renders WITH palette-token colors (not a named theme, and
	// not plain text): the body survives and SGR colors are present. chroma
	// splits tokens with SGR resets, so assert on the stripped body plus the
	// presence of color codes.
	md := &model{theme: th, md: newMDCacheFromTheme(th)}
	out := md.renderMarkdown("```go\npackage main\n```\n", 60, false)
	if !strings.Contains(xansi.Strip(out), "package main") {
		t.Fatalf("code body missing: %q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;") {
		t.Fatalf("palette-token highlighting missing: %q", out)
	}
}

func TestModelWiresThemeFromConfig(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("MOW_FORCE_COLOR", "1")

	m := newModel(testEngine(t), false, false)
	th := newThemeFrom(ThemeConfig{
		Name:   "monokai",
		Colors: map[string]string{"accent": "#FFD866"},
	}, true)
	m.theme = th
	m.md = newMDCacheFromTheme(th)

	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.showWelcome = false
	m.add(kindError, "theme boom")
	m.refreshVP()
	view := m.View().Content
	if !strings.Contains(view, "theme boom") {
		t.Fatalf("missing error text: %q", view)
	}
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("expected ANSI in monokai view: %q", firstColorSeq(view))
	}

	// Accent render carries the override RGB (255,216,102).
	assertRGB(t, th.Accent.Render("x"), 255, 216, 102)
}

func TestWelcomeUsesThemeChrome(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("MOW_FORCE_COLOR", "1")

	th := newThemeFrom(ThemeConfig{
		Name:   "monokai",
		Colors: map[string]string{"accent": "#AB9DF2"},
	}, true)
	m := newModel(testEngine(t), false, false)
	m.theme = th
	on := true
	m.cfg.Welcome = &on
	m.showWelcome = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View().Content
	if !strings.Contains(strings.ToLower(view), "mowi") {
		t.Fatalf("welcome missing brand: %q", view)
	}
	// Accent purple #AB9DF2 → 171;157;242
	if !strings.Contains(view, "171;157;242") {
		// Soft: welcome title uses Accent; if lipgloss profile differs, still require some SGR.
		if !strings.Contains(view, "\x1b[38;") {
			t.Fatalf("welcome missing color SGR: %q", firstColorSeq(view))
		}
	}
}

func TestStatusBarUsesTheme(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("MOW_FORCE_COLOR", "1")

	m := newModel(testEngine(t), false, false)
	th := newThemeFrom(ThemeConfig{Name: "monokai"}, true)
	m.theme = th
	m.md = newMDCacheFromTheme(th)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.showWelcome = false
	m.refreshVP()
	view := m.View().Content
	if !strings.Contains(view, "mowi") {
		t.Fatalf("status missing brand: %q", view)
	}
}

func TestApplyColorOverridesEmpty(t *testing.T) {
	p := defaultPalette(true)
	got := applyColorOverrides(p, nil)
	if got != p {
		t.Fatal("nil colors should be identity")
	}
	got = applyColorOverrides(p, map[string]string{})
	if got != p {
		t.Fatal("empty colors should be identity")
	}
	// Unknown keys are ignored.
	got = applyColorOverrides(p, map[string]string{"nope": "#ffffff"})
	if got != p {
		t.Fatal("unknown key mutated palette")
	}
}

func TestDefaultPaletteLightDarkDistinct(t *testing.T) {
	d := defaultPalette(true)
	l := defaultPalette(false)
	if d == l {
		t.Fatal("light and dark default palettes must differ")
	}
	if d.userBg == l.userBg || d.fg == l.fg {
		t.Fatalf("expected distinct fg/userBg dark=%+v light=%+v", d, l)
	}
}

func TestLoadConfigThemeRoundTrip(t *testing.T) {
	c := LoadConfigRaw(func(name string, dst any) error {
		if name != "tui" {
			return nil
		}
		*dst.(*Config) = Config{
			Theme: ThemeConfig{
				Name: "monokai",
				Colors: map[string]string{
					"accent":  "#FFD866",
					"user_bg": "#221F22",
				},
			},
			Prompt: "›",
		}
		return nil
	})
	if NormalizeThemeName(c.Theme.Name) != "monokai" {
		t.Fatalf("name=%q", c.Theme.Name)
	}
	if c.Theme.Colors["accent"] != "#FFD866" {
		t.Fatalf("colors=%v", c.Theme.Colors)
	}
	th := newThemeFrom(c.Theme, true)
	if th.name != "monokai" || !th.mdDark || th.chromaStyle != "" {
		t.Fatalf("theme meta name=%s mdDark=%v chroma=%s", th.name, th.mdDark, th.chromaStyle)
	}
	if !strings.EqualFold(th.palette.accent, "#FFD866") {
		t.Fatalf("accent=%q", th.palette.accent)
	}
	if c.PromptPrefix() != "› " {
		t.Fatalf("prompt=%q", c.PromptPrefix())
	}
}

func TestThemeConfigFromYAMLShape(t *testing.T) {
	// Mirrors config_test.TestThemeMonokaiFromYAML but covers light-term pin + overrides.
	c := LoadConfigRaw(func(name string, dst any) error {
		if name != "tui" {
			return nil
		}
		*dst.(*Config) = Config{
			Theme: ThemeConfig{
				Name:   "monokai",
				Colors: map[string]string{"accent": "#FFD866", "error": "#FF6188"},
			},
		}
		return nil
	})
	// Light terminal still gets monokai dark chrome.
	th := newThemeFrom(c.Theme, false)
	if th.name != "monokai" || !th.mdDark {
		t.Fatalf("theme=%s mdDark=%v", th.name, th.mdDark)
	}
	if !strings.EqualFold(th.palette.accent, "#FFD866") {
		t.Fatalf("accent=%q", th.palette.accent)
	}
	if !strings.EqualFold(th.palette.err, "#FF6188") {
		t.Fatalf("err=%q", th.palette.err)
	}
}

// assertRGB checks that s contains a truecolor SGR with the given RGB.
func assertRGB(t *testing.T, s string, r, g, b int) {
	t.Helper()
	needle := strings.ReplaceAll(
		strings.ReplaceAll(
			strings.ReplaceAll("R;G;B", "R", itoa(r)),
			"G", itoa(g),
		),
		"B", itoa(b),
	)
	if !strings.Contains(s, needle) {
		t.Fatalf("missing RGB %s in %q", needle, s)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func ptrStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// Keep lipgloss import used (style GetForeground nil checks).
var _ = lipgloss.NewStyle

// Light built-in palette: slash must differ from warn (was identical #B45309),
// so /commands and warnings are distinguishable; warn keeps its amber.
func TestLightPaletteSlashDistinctFromWarn(t *testing.T) {
	p := defaultPalette(false)
	if p.slash == p.warn {
		t.Errorf("light slash == warn (%s): /commands indistinguishable from warnings", p.slash)
	}
	if got, want := p.slash, "#C2410C"; got != want {
		t.Errorf("light slash = %q, want %q (orange-700, 4.6:1 on white)", got, want)
	}
}

// Chroma-derived chrome border must never fuse with the terminal background:
// github's 0.20 wash lands at #f3f4f5 (1.04:1 on white) and rules vanish.
func TestChromaDerivedBorderVisible(t *testing.T) {
	p, dark, ok := paletteFromChroma("github")
	if !ok || dark {
		t.Fatalf("github palette: ok=%v dark=%v", ok, dark)
	}
	// Same Background derivation paletteFromChroma uses (bg is near-white).
	bg := chromastyles.Get("github").Get(chroma.Background).Background.String()
	if got := contrastRatio(p.border, bg); got < 1.30 {
		t.Errorf("github border %s vs bg %s contrast %.2f < 1.30 (invisible rules)", p.border, bg, got)
	}
	// Dark styles keep the subtle 0.20 wash unless it actually fuses.
	pm, dark, ok := paletteFromChroma("catppuccin-mocha")
	if !ok || !dark {
		t.Fatalf("catppuccin-mocha palette: ok=%v dark=%v", ok, dark)
	}
	if got := contrastRatio(pm.border, "#1e1e2e"); got < 1.15 {
		t.Errorf("catppuccin-mocha border %s contrast %.2f — should stay subtle but visible", pm.border, got)
	}
}
