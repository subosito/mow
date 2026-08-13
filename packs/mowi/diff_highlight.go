package mowi

import (
	"path/filepath"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	xansi "github.com/charmbracelet/x/ansi"
)

// diffHighlighter applies restrained, file-aware Chroma foregrounds to a
// single source line. Semantic add/delete styling stays on the caller's
// body style (background wash + base fg); this layer only injects token
// foregrounds so keywords stay legible without drowning the green/red band.
//
// Diff syntax always uses the theme palette (not theme.code fence splash):
// fenced monokai/etc. can paint pure white identifiers, which is harsh on a
// review band. Unknown lexer / NO_COLOR → paint returns "" (plain base style).
type diffHighlighter struct {
	style *chroma.Style
	mu    sync.Mutex
	cache map[string]chroma.Lexer // path → lexer (nil entry = miss remembered)
}

func newDiffHighlighter(th theme) *diffHighlighter {
	if noColor() {
		return &diffHighlighter{}
	}
	// Always palette-derived for diffs: theme-integrated and restrained.
	// (theme.code still drives markdown fences; diffs need quieter ink.)
	st := paletteChromaStyle(th.palette)
	if st == nil {
		return &diffHighlighter{}
	}
	return &diffHighlighter{style: st, cache: make(map[string]chroma.Lexer)}
}

// paletteChromaStyle builds a quiet chroma style from the theme palette.
// Base text uses softDiffInk (not full fg) so identifiers are not harsh white;
// keywords/strings/numbers keep accent/meta for scanability.
func paletteChromaStyle(p palette) *chroma.Style {
	soft := softDiffInk(p)
	if soft == "" && p.fg == "" {
		return nil
	}
	if soft == "" {
		soft = p.fg
	}
	sb := chroma.NewStyleBuilder("mow-diff")
	add := func(t chroma.TokenType, hex string) {
		if hex == "" {
			return
		}
		_ = sb.Add(t, "#"+strings.TrimPrefix(hex, "#"))
	}
	// Quiet base — plain identifiers recede.
	add(chroma.Text, soft)
	add(chroma.Name, soft)
	add(chroma.NameBuiltin, soft)
	add(chroma.NameFunction, soft)
	add(chroma.NameVariable, soft)
	add(chroma.NameOther, soft)
	// Structure and literals stay scannable but not neon.
	add(chroma.Comment, p.muted)
	add(chroma.CommentPreproc, p.muted)
	add(chroma.Keyword, p.accent)
	add(chroma.KeywordReserved, p.accent)
	add(chroma.KeywordNamespace, p.meta)
	add(chroma.KeywordType, p.meta)
	add(chroma.Operator, p.muted)
	add(chroma.Punctuation, p.muted)
	add(chroma.NameTag, p.accent)
	add(chroma.NameAttribute, p.meta)
	add(chroma.NameClass, p.accent)
	add(chroma.NameDecorator, p.meta)
	add(chroma.NameConstant, p.meta)
	add(chroma.LiteralNumber, p.meta)
	add(chroma.LiteralString, p.meta)
	add(chroma.LiteralStringEscape, p.accent)
	s, _ := sb.Build()
	return s
}

// lexerFor returns a chroma lexer for path, or nil when unknown.
// Does not fall back to Analyse/Fallback — a wrong language is worse than none.
func (h *diffHighlighter) lexerFor(path string) chroma.Lexer {
	if h == nil || path == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cache == nil {
		h.cache = make(map[string]chroma.Lexer)
	}
	if lx, ok := h.cache[path]; ok {
		return lx
	}
	base := filepath.Base(path)
	var lx chroma.Lexer
	if l := lexers.Match(base); l != nil {
		lx = chroma.Coalesce(l)
	} else if ext := filepath.Ext(base); ext != "" {
		if l := lexers.Get(strings.TrimPrefix(ext, ".")); l != nil {
			lx = chroma.Coalesce(l)
		} else if a, ok := langAliases[strings.ToLower(strings.TrimPrefix(ext, "."))]; ok {
			if l := lexers.Get(a); l != nil {
				lx = chroma.Coalesce(l)
			}
		}
	}
	h.cache[path] = lx
	return lx
}

// paintSeated tokenises one source line and renders each token with chroma
// foregrounds seated on base (add/del/ctx band). Returns "" when highlighting
// is unavailable so the caller can fall back to base.Render(body).
//
// Seating is intentional: the TTY16m formatter emits full SGR resets that wipe
// a pre-applied background, which would erase the green/red wash that marks
// change direction. Token-by-token lipgloss keeps the band bg and only swaps FG.
func (h *diffHighlighter) paintSeated(path, line string, base lipgloss.Style) string {
	if h == nil || h.style == nil || noColor() {
		return ""
	}
	lx := h.lexerFor(path)
	if lx == nil {
		return ""
	}
	// Line-oriented lex is good enough for diff rows (multi-line constructs
	// may color slightly off; acceptable for a restrained overlay).
	it, err := lx.Tokenise(nil, line)
	if err != nil {
		return ""
	}
	tokens := it.Tokens()
	if len(tokens) == 0 {
		return ""
	}
	var b strings.Builder
	colored := false
	for _, tok := range tokens {
		if tok.Value == "" {
			continue
		}
		st := base
		if e := h.style.Get(tok.Type); e.Colour.IsSet() {
			st = base.Foreground(lipgloss.Color(e.Colour.String()))
			colored = true
		}
		b.WriteString(st.Render(tok.Value))
	}
	if !colored {
		// Lexer produced only unstyled text — not useful as a highlight layer.
		return ""
	}
	return b.String()
}

// paintDiffBody paints syntax foregrounds seated on the row's semantic band
// (add/del/ctx). When paint fails, returns base.Render(body). Diff signs and
// number tint stay in the gutter (formatDiffRowPre); this only owns the body.
//
// width > 0 pads the body to that many display cells with base so the band
// wash spans the full content area (split cells). width 0 leaves padding to
// the caller (formatDiffRowPre).
func paintDiffBody(th theme, hl *diffHighlighter, path, body string, base lipgloss.Style, width int) string {
	if body == "" {
		body = " "
	}
	body = expandDiffTabs(body, 4)
	painted := ""
	// Flashdiff only syntax-highlights context. Add/del rows keep a single
	// accent on the sunk band — seating muted token greys on those rows is
	// what made mocha unreadable.
	if hl != nil && path != "" && isDiffCtxStyle(th, base) {
		painted = hl.paintSeated(path, body, base)
	}
	if painted == "" {
		painted = base.Render(body)
	}
	if width <= 0 {
		return painted
	}
	// Pad to width so the band is a rectangle, not a stripe.
	vis := xansi.StringWidth(xansi.Strip(painted))
	if pad := width - vis; pad > 0 {
		painted += base.Render(strings.Repeat(" ", pad))
	}
	return painted
}

func isDiffCtxStyle(th theme, base lipgloss.Style) bool {
	// Context has no wash; add/del always carry a band background.
	_ = th
	return base.GetBackground() == nil
}

// paintDiffBodySegs paints a body from word-diff segments: changed tokens use
// the inverted word chip; shared tokens use soft base (de-emphasised). No
// chroma on segmented rows — the chip is the primary signal.
//
// width > 0 pads with soft so the band spans the cell.
func paintDiffBodySegs(soft, word lipgloss.Style, segs []diffSeg, width int) string {
	if len(segs) == 0 {
		return soft.Render(" ")
	}
	var b strings.Builder
	for _, s := range segs {
		if s.text == "" {
			continue
		}
		if s.changed {
			b.WriteString(word.Render(s.text))
		} else {
			b.WriteString(soft.Render(s.text))
		}
	}
	out := b.String()
	if out == "" {
		out = soft.Render(" ")
	}
	if width > 0 {
		if pad := width - xansi.StringWidth(out); pad > 0 {
			out += soft.Render(strings.Repeat(" ", pad))
		}
	}
	return out
}
