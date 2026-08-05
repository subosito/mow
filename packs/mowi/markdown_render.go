package mowi

import (
	"io"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	goldext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
)

// mdStyle is the precomputed visual vocabulary of the custom renderer,
// derived once from the TUI theme palette (never OSC-probed).
type mdStyle struct {
	fg, muted, accent, meta, border, userBg string
	chromaName                              string // named chroma theme, "" = palette tokens
}

// mdStyleFaint returns a dimmed copy of st for low-priority progress
// surfaces (live peer bodies, thinking): every palette token is mixed toward
// the terminal background so the text reads as secondary, never as main
// content. Named chroma themes are dropped — the dimmed palette tokens paint
// instead, so nothing bypasses the decoration.
func mdStyleFaint(st mdStyle, dark bool) mdStyle {
	bg := "#FFFFFF" // light: mute toward the page
	if dark {
		bg = "#000000"
	}
	return mdStyle{
		fg:         mixHex(st.fg, bg, 0.55),
		muted:      mixHex(st.muted, bg, 0.55),
		accent:     mixHex(st.accent, bg, 0.55),
		meta:       mixHex(st.meta, bg, 0.55),
		border:     mixHex(st.border, bg, 0.55),
		userBg:     mixHex(st.userBg, bg, 0.55),
		chromaName: "",
	}
}

func mdStyleFromPalette(p palette, dark bool, chromaName string) mdStyle {
	return mdStyle{
		fg:         p.fg,
		muted:      p.muted,
		accent:     p.accent,
		meta:       p.meta,
		border:     p.border,
		userBg:     p.userBg,
		chromaName: chromaName,
	}
}

func hexRGB(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0
	}
	hp := func(s string) int {
		v := 0
		for _, c := range s {
			v *= 16
			switch {
			case c >= '0' && c <= '9':
				v += int(c - '0')
			case c >= 'a' && c <= 'f':
				v += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v += int(c-'A') + 10
			}
		}
		return v
	}
	return hp(hex[0:2]), hp(hex[2:4]), hp(hex[4:6])
}

// sgr wraps s in SGR codes: optional 24-bit fg, optional 24-bit bg, and
// bold/italic/strike flags. Bare text passes through unchanged.
func sgr(s, fg, bg string, bold, italic, strike, underline bool) string {
	if s == "" {
		return ""
	}
	var codes []string
	if fg != "" {
		r, g, b := hexRGB(fg)
		codes = append(codes, "38;2;"+strconv.Itoa(r)+";"+strconv.Itoa(g)+";"+strconv.Itoa(b))
	}
	if bg != "" {
		r, g, b := hexRGB(bg)
		codes = append(codes, "48;2;"+strconv.Itoa(r)+";"+strconv.Itoa(g)+";"+strconv.Itoa(b))
	}
	if bold {
		codes = append(codes, "1")
	}
	if italic {
		codes = append(codes, "3")
	}
	if strike {
		codes = append(codes, "9")
	}
	if underline {
		codes = append(codes, "4")
	}
	if len(codes) == 0 {
		return s
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + s + "\x1b[0m"
}

// mdRenderer renders a goldmark AST straight to styled, width-wrapped
// transcript text. Inline content accumulates per paragraph so wrapping is
// display-width aware and block prefixes (quotes, list markers) apply after
// the wrap. Deliberately small: no glamour-style document chrome or per-token
// style machinery.
type mdListItemState struct {
	marker string
	indent string
	inItem bool
}

type mdRenderer struct {
	st    mdStyle
	width int

	// inline style depth counters (goldmark nests emphasis/strong).
	bold, em, strike, link, heading int

	// paragraph accumulation (current inline target).
	para       strings.Builder
	itemMarker string // pending list marker for the next paragraph line
	paraIndent string // hanging indent for continuation lines in list items

	// block context.
	quoteDepth int
	hugNext    bool  // heading just flushed compact: next block hugs it (no air)
	orderStack []int // per-list-level ordered counters; 0 = unordered level
	inListItem bool  // inside a list item: flush compact (no blank between items)
	listItems  []mdListItemState

	// table context: rows accumulate until the table ends so every column is
	// padded to the widest cell across ALL rows (header + body), keeping the
	// │ separators vertically aligned.
	inTable   bool
	headerRow int // index into tableRows of the header row; -1 = none
	tableRows [][]string
	tableRow  []string
	cellBuf   *strings.Builder // non-nil while collecting a table cell

	// chromaStyle is built lazily once per render (multi-fence docs otherwise
	// rebuild the style builder for every block).
	chromaOnce bool
	chromaVal  *chroma.Style

	// finished output lines.
	outBuf strings.Builder
}

var _ renderer.Renderer = (*mdRenderer)(nil)

func newMDRenderer(st mdStyle, width int) renderer.Renderer {
	return &mdRenderer{st: st, width: width}
}

func (r *mdRenderer) AddOptions(...renderer.Option) {}

func (r *mdRenderer) Render(w io.Writer, source []byte, node ast.Node) error {
	if err := ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		return r.walk(source, n, entering)
	}); err != nil {
		return err
	}
	_, err := io.WriteString(w, cleanLines(r.outBuf.String()))
	return err
}

// append writes styled inline text to the active target: a table cell buffer
// while collecting a cell, else the paragraph buffer.
func (r *mdRenderer) append(s, fg string, bold, italic, strike bool) {
	if s == "" {
		return
	}
	if r.heading > 0 {
		// Heading hierarchy: 1-3 accent bold, 4-5 muted bold, 6 muted plain
		// (mirrors the old glamour style config H1-H6 mapping).
		switch {
		case r.heading <= 3:
			fg = r.st.accent
			bold = true
		case r.heading <= 5:
			fg = r.st.muted
			bold = true
		default:
			fg = r.st.muted
			bold = false
		}
	}
	if r.link > 0 && fg == "" {
		fg = r.st.accent
		bold = true
	}
	if fg == "" && r.quoteDepth > 0 {
		fg = r.st.muted // quoted text reads quieter; the gutter already marks it
	}
	styled := sgr(s, fg, "", bold || r.bold > 0, italic || r.em > 0, strike || r.strike > 0, false)
	if r.cellBuf != nil {
		r.cellBuf.WriteString(styled)
		return
	}
	r.para.WriteString(styled)
}

// flushPara wraps the accumulated paragraph to the render width, applies the
// pending list marker + hanging indent + quote prefix, and emits it. It is a
// no-op inside a table cell (cells are single-line).
func (r *mdRenderer) flushPara() { r.flushParaBlank(true) }

// flushParaBlank is flushPara with control over the trailing blank line.
// List items flush compact (blank=false) so sibling items sit on consecutive
// lines — glamour's tight list rhythm — while prose blocks keep their air.
func (r *mdRenderer) flushParaBlank(blank bool) {
	if r.cellBuf != nil {
		r.para.Reset()
		return
	}
	raw := r.para.String()
	r.para.Reset()
	if strings.TrimSpace(xansi.Strip(raw)) == "" {
		return
	}
	// Prose blocks need air above them when the previous block was compact
	// (list items flush without a trailing blank line). A heading is the
	// exception: it titles what follows, so its body sits directly beneath.
	hug := r.hugNext
	r.hugNext = false
	if blank && !hug && r.outBuf.Len() > 0 && !strings.HasSuffix(r.outBuf.String(), "\n\n") {
		r.outBuf.WriteByte('\n')
	}
	lines := strings.Split(raw, "\n")
	// The quote gutter applies to every line; the list hanging indent applies
	// to continuation lines only (the marker line carries its own indent).
	quote := ""
	if r.quoteDepth > 0 {
		quote = strings.Repeat(sgr("│ ", r.st.muted, "", false, false, false, false), r.quoteDepth)
	}
	first := true
	for _, ln := range lines {
		if strings.TrimSpace(xansi.Strip(ln)) == "" {
			continue
		}
		if first && r.itemMarker != "" {
			ln = r.itemMarker + ln
			r.itemMarker = ""
		}
		lead := quote
		if !first {
			lead += r.paraIndent
		}
		w := max(8, r.width)
		if lead != "" {
			w = max(8, r.width-xansi.StringWidth(lead))
		}
		r.outBuf.WriteString(lead + wordWrap(ln, w))
		r.outBuf.WriteByte('\n')
		first = false
	}
	if blank {
		r.outBuf.WriteByte('\n')
	}
}

func (r *mdRenderer) emit(line string) {
	r.outBuf.WriteString(line)
	r.outBuf.WriteByte('\n')
}

// walk dispatches on node kind. Block nodes manage blank-line separation and
// accumulation boundaries; inline nodes append styled text.
func (r *mdRenderer) walk(source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	switch v := n.(type) {
	case *ast.Document:
		return ast.WalkContinue, nil

	case *ast.Paragraph, *ast.TextBlock:
		if entering {
			r.para.Reset()
		} else {
			// Paragraphs in a list item are emitted when they close; otherwise
			// a nested list can overwrite the parent item's pending marker.
			r.flushParaBlank(!r.inListItem)
		}
		return ast.WalkContinue, nil

	case *ast.Text:
		if !entering {
			return ast.WalkContinue, nil
		}
		txt := sanitizeDisplay(string(v.Segment.Value(source)))
		if v.IsRaw() {
			r.append(txt, "", false, false, false)
		} else {
			if v.SoftLineBreak() || v.HardLineBreak() {
				txt = "\n" + txt
			}
			r.append(txt, "", false, false, false)
		}
		return ast.WalkContinue, nil

	case *ast.RawHTML:
		if !entering {
			return ast.WalkContinue, nil
		}
		r.append(sanitizeDisplay(stripHTMLTags(string(v.Segments.Value(source)))), "", false, false, false)
		return ast.WalkContinue, nil

	case *ast.String:
		if !entering {
			return ast.WalkContinue, nil
		}
		r.append(sanitizeDisplay(string(v.Value)), "", false, false, false)
		return ast.WalkContinue, nil

	case *ast.Heading:
		if entering {
			r.heading = v.Level
			r.para.Reset()
			// Block rhythm: headings stand apart from what precedes them,
			// even after a compact list item (glamour spaces around headings).
			if r.outBuf.Len() > 0 && !strings.HasSuffix(r.outBuf.String(), "\n\n") {
				r.outBuf.WriteByte('\n')
			}
		} else {
			r.heading = 0
			// Hug: blank line above the heading, none below — a heading
			// belongs to its body, not to the block that precedes it.
			r.flushParaBlank(false)
			r.hugNext = true
		}
		return ast.WalkContinue, nil

	case *ast.CodeSpan:
		if !entering {
			return ast.WalkContinue, nil
		}
		txt := strings.TrimSpace(sanitizeDisplay(string(v.Text(source))))
		// No left/right padding — inline code prints flush with the surrounding
		// text (the underline is gone, so nothing needs breathing space).
		r.append(sgr(txt, r.st.fg, "", false, false, false, false), "", false, false, false)
		return ast.WalkSkipChildren, nil

	case *ast.Emphasis:
		// goldmark models *strong* as Emphasis{Level:2}.
		if entering {
			if v.Level >= 2 {
				r.bold++
			} else {
				r.em++
			}
		} else {
			if v.Level >= 2 {
				r.bold--
			} else {
				r.em--
			}
		}
		return ast.WalkContinue, nil

	case *goldext.Strikethrough:
		if entering {
			r.strike++
		} else {
			r.strike--
		}
		return ast.WalkContinue, nil

	case *ast.Link:
		if entering {
			r.link++
		} else {
			r.link--
		}
		return ast.WalkContinue, nil

	case *ast.AutoLink:
		if !entering {
			return ast.WalkContinue, nil
		}
		r.append(sanitizeDisplay(string(v.URL(source))), r.st.meta, false, true, false)
		return ast.WalkSkipChildren, nil

	case *ast.Image:
		if entering {
			r.append("[image: ", r.st.muted, false, false, false)
		} else {
			r.append("]", r.st.muted, false, false, false)
		}
		return ast.WalkContinue, nil

	case *ast.FencedCodeBlock:
		if !entering {
			return ast.WalkContinue, nil
		}
		r.flushPara()
		r.writeCodeBlock(string(v.Language(source)), sanitizeDisplay(nodeText(v, source)))
		return ast.WalkSkipChildren, nil

	case *ast.HTMLBlock:
		if !entering {
			return ast.WalkContinue, nil
		}
		r.flushPara()
		lines := v.Lines()
		var sb strings.Builder
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			sb.Write(seg.Value(source))
		}
		// Strip tags for display safety; keep line structure.
		body := sanitizeDisplay(stripHTMLTags(sb.String()))
		if strings.TrimSpace(body) != "" {
			for _, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				r.emit(ln)
			}
			r.emit("")
		}
		return ast.WalkSkipChildren, nil

	case *ast.CodeBlock:
		if !entering {
			return ast.WalkContinue, nil
		}
		r.flushPara()
		r.writeCodeBlock("", sanitizeDisplay(nodeText(v, source)))
		return ast.WalkSkipChildren, nil

	case *ast.ThematicBreak:
		if entering {
			r.flushPara()
			r.emit(sgr(strings.Repeat("\u2500", max(4, r.width)), r.ruleColor(), "", false, false, false, false))
			r.emit("")
		}
		return ast.WalkSkipChildren, nil

	case *ast.Blockquote:
		if entering {
			r.quoteDepth++
		} else {
			r.quoteDepth--
		}
		return ast.WalkContinue, nil

	case *ast.List:
		if entering {
			if v.IsOrdered() {
				r.orderStack = append(r.orderStack, max(1, v.Start))
			} else {
				r.orderStack = append(r.orderStack, 0)
			}
		} else {
			r.orderStack = r.orderStack[:max(0, len(r.orderStack)-1)]
		}
		return ast.WalkContinue, nil

	case *ast.ListItem:
		if entering {
			r.listItems = append(r.listItems, mdListItemState{r.itemMarker, r.paraIndent, r.inListItem})
			level := len(r.orderStack)
			// Nest 4 cells per level, not 2: a 2-cell step puts a child
			// marker at the same column as the parent's wrapped
			// continuation lines, so nesting reads as a wrap. 4 cells
			// makes the hierarchy unmistakable at two levels deep.
			indent := strings.Repeat("    ", max(0, level-1))
			if n := len(r.orderStack); n > 0 && r.orderStack[n-1] > 0 {
				num := r.orderStack[n-1]
				r.orderStack[n-1] = num + 1
				r.itemMarker = sgr(indent+strconv.Itoa(num)+". ", r.st.accent, "", false, false, false, false)
			} else {
				r.itemMarker = sgr(indent+"• ", r.st.accent, "", false, false, false, false)
			}
			r.paraIndent = strings.Repeat(" ", xansi.StringWidth(xansi.Strip(r.itemMarker)))
			r.para.Reset()
			r.inListItem = true
		} else {
			r.flushParaBlank(false) // handles list items without a paragraph child
			last := len(r.listItems) - 1
			if last >= 0 {
				prev := r.listItems[last]
				r.listItems = r.listItems[:last]
				r.itemMarker, r.paraIndent, r.inListItem = prev.marker, prev.indent, prev.inItem
			}
		}
		return ast.WalkContinue, nil

	case *goldext.TaskCheckBox:
		if entering {
			if v.IsChecked {
				r.append(sgr("[x] ", r.st.accent, "", true, false, false, false), "", false, false, false)
			} else {
				r.append(sgr("[ ] ", r.st.muted, "", false, false, false, false), "", false, false, false)
			}
		}
		return ast.WalkContinue, nil

	case *goldext.Table:
		if entering {
			r.flushPara()
			r.inTable = true
			r.tableRows = nil
			r.headerRow = -1
		} else {
			r.inTable = false
			r.emitTable()
			r.emit("")
		}
		return ast.WalkContinue, nil

	case *goldext.TableHeader:
		// goldmark's header holds its cells directly (no TableRow wrapper).
		if entering {
			r.tableRow = nil
			return ast.WalkContinue, nil
		}
		r.headerRow = len(r.tableRows)
		r.tableRows = append(r.tableRows, r.tableRow)
		return ast.WalkContinue, nil

	case *goldext.TableRow:
		if entering {
			r.tableRow = nil
		} else {
			r.tableRows = append(r.tableRows, r.tableRow)
		}
		return ast.WalkContinue, nil

	case *goldext.TableCell:
		if entering {
			r.cellBuf = &strings.Builder{}
			return ast.WalkContinue, nil
		}
		if r.cellBuf != nil {
			r.tableRow = append(r.tableRow, strings.TrimSpace(xansi.Strip(r.cellBuf.String())))
			r.cellBuf = nil
		}
		return ast.WalkContinue, nil

	default:
		return ast.WalkContinue, nil
	}
}

// stripHTMLTags removes markup from RawHTML nodes while preserving their text.
// Goldmark has already identified these as HTML, so this intentionally small
// scanner need not interpret ordinary markdown text.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nodeText concatenates a code block's line segments.
func nodeText(v interface{ Lines() *text.Segments }, source []byte) string {
	var sb strings.Builder
	segs := v.Lines()
	for i := 0; i < segs.Len(); i++ {
		seg := segs.At(i)
		sb.Write(seg.Value(source))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ruleColor picks the horizontal-rule ink. Muted is intentionally darker than
// border: a full-width rule carries far more visual weight than a 1-cell box
// edge, so border ink reads as a light-mode whiteout (github-style themes mix
// border within a few points of the terminal background) and washes out even
// in the built-in light palette. Chrome boxes still use border — the surfaces
// are small enough for the subtle token to work there.
func (r *mdRenderer) ruleColor() string {
	if r.st.muted != "" {
		return r.st.muted
	}
	return r.st.border
}

// writeCodeBlock highlights via chroma (palette tokens or a named theme) and
// emits each line indented by codeIndentCells, clipped to the render width.
func (r *mdRenderer) writeCodeBlock(lang, code string) {
	if strings.TrimSpace(code) == "" {
		return
	}
	lexer := resolveLexer(lang, code)
	style := r.chromaStyle()
	it, err := lexer.Tokenise(nil, code)
	body := code
	if err == nil {
		var sb strings.Builder
		if ferr := formatters.Get("terminal16m").Format(&sb, style, it); ferr == nil {
			body = sb.String()
		}
	}
	// Fill-free code region: a muted rail on every line. Uses the LIGHT
	// vertical │ (U+2502) — the heavy ┃ (U+2503) renders as a double stroke
	// ("two rails") in many terminal fonts. │ is the same single clean stroke
	// the blockquote gutter uses; no background wash anywhere.
	rail := sgr("│ ", r.st.muted, "", false, false, false, false)
	maxW := max(8, r.width-2)
	for _, ln := range strings.Split(body, "\n") {
		if xansi.StringWidth(ln) > maxW-2 {
			ln = xansi.Truncate(ln, maxW-2, "…")
		}
		r.emit(rail + ln)
	}
	r.emit("")
}

func (r *mdRenderer) chromaStyle() *chroma.Style {
	if r.chromaOnce {
		return r.chromaVal
	}
	r.chromaOnce = true
	if r.st.chromaName != "" {
		if s := styles.Get(r.st.chromaName); s != nil {
			return s
		}
	}
	sb := chroma.NewStyleBuilder("mow")
	added := false
	add := func(t chroma.TokenType, hex string) {
		if hex == "" {
			return
		}
		// Palette tokens carry their own "#" — normalize so chroma never sees
		// "##E5E7EB" (ParseStyleEntry rejects it, Build() fails, and the whole
		// style silently falls back to plain text).
		_ = sb.Add(t, "#"+strings.TrimPrefix(hex, "#"))
		added = true
	}
	add(chroma.Text, r.st.fg)
	add(chroma.Comment, r.st.muted)
	add(chroma.CommentPreproc, r.st.muted)
	add(chroma.Keyword, r.st.accent)
	add(chroma.KeywordReserved, r.st.accent)
	add(chroma.KeywordNamespace, r.st.meta)
	add(chroma.KeywordType, r.st.meta)
	add(chroma.Operator, r.st.muted)
	add(chroma.Punctuation, r.st.muted)
	add(chroma.Name, r.st.fg)
	add(chroma.NameBuiltin, r.st.fg)
	add(chroma.NameTag, r.st.accent)
	add(chroma.NameAttribute, r.st.meta)
	add(chroma.NameClass, r.st.accent)
	add(chroma.NameDecorator, r.st.meta)
	add(chroma.NameFunction, r.st.fg)
	add(chroma.LiteralNumber, r.st.meta)
	add(chroma.LiteralString, r.st.meta)
	add(chroma.LiteralStringEscape, r.st.accent)
	add(chroma.GenericDeleted, r.st.meta)
	add(chroma.GenericInserted, r.st.meta)
	// Background: pin code text to the terminal colors. When the palette
	// drives colors (custom theme.colors), chroma's default Text/Background
	// colors would otherwise paint bright black-on-white or white-on-black
	// noise that fights both the terminal and NO_COLOR. Entry format is
	// "foreground bg:background" — a bare "bg:x bg:y" has no foreground and
	// makes chroma's Build() fail (style comes back nil).
	if r.st.fg != "" && r.st.userBg != "" {
		sb.Add(chroma.Background, r.st.fg)
		added = true
	}
	s, _ := sb.Build()
	if !added || s == nil {
		// Blank palette (NO_COLOR) or a failed build: no style at all, so
		// nothing can paint — code falls back to the muted plain path.
		return nil
	}
	r.chromaVal = s
	return s
}

// writeTableRow emits one table row: cells joined by " │ "; header rows are
// accent-bold.
func (r *mdRenderer) emitTable() {
	if len(r.tableRows) == 0 {
		return
	}
	ncols := 0
	for _, row := range r.tableRows {
		if len(row) > ncols {
			ncols = len(row)
		}
	}
	if ncols == 0 {
		return
	}
	widths := make([]int, ncols)
	for _, row := range r.tableRows {
		for i, c := range row {
			if w := xansi.StringWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	for ri, row := range r.tableRows {
		cells := make([]string, ncols)
		for i := 0; i < ncols; i++ {
			c := ""
			if i < len(row) {
				c = row[i]
			}
			pad := strings.Repeat(" ", widths[i]-xansi.StringWidth(c))
			if ri == r.headerRow {
				cells[i] = " " + sgr(c, r.st.accent, "", true, false, false, false) + pad + " "
			} else {
				cells[i] = " " + c + pad + " "
			}
		}
		line := strings.Join(cells, "│")
		w := max(8, r.width)
		if xansi.StringWidth(line) > w {
			line = xansi.Truncate(line, w, "…")
		}
		r.emit(line)
		if ri == r.headerRow {
			segs := make([]string, ncols)
			for i, cw := range widths {
				segs[i] = strings.Repeat("─", cw+2) // spans the cell padding
			}
			r.emit(sgr(strings.Join(segs, "│"), r.st.muted, "", false, false, false, false))
		}
	}
}

// cleanLines trims outer blank lines and per-line trailing whitespace.
func cleanLines(s string) string {
	s = strings.TrimRight(s, "\n")
	s = strings.TrimLeft(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	var out []string
	for _, ln := range lines {
		out = append(out, strings.TrimRight(ln, " \t"))
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// mdCache is the renderer cache keyed by wrap width (kept API). Rendering is
// stateless per call (goldmark instance built per render), so no lock needed.
type mdCache struct {
	st mdStyle
}

// newMDCache builds a renderer cache for the default adaptive palette (tests).
func newMDCache(dark bool) mdCache {
	p := defaultPalette(dark)
	return mdCache{st: mdStyleFromPalette(p, dark, "")}
}

// newMDCacheFromTheme uses the same hex tokens as chrome (accent/fg/muted/…).
func newMDCacheFromTheme(th theme) mdCache {
	return mdCache{st: mdStyleFromPalette(th.palette, th.mdDark, th.chromaStyle)}
}

// newMDCacheFaintFromTheme builds a dimmed renderer cache for low-priority
// progress surfaces (live peer bodies): same structure as the main cache but
// every token is muted toward the terminal background.
func newMDCacheFaintFromTheme(th theme) mdCache {
	return mdCache{st: mdStyleFaint(mdStyleFromPalette(th.palette, th.mdDark, th.chromaStyle), th.mdDark)}
}

func (c *mdCache) render(md string, width int) (string, error) {
	// Leave a little slack so styled lines never exceed the viewport cell
	// width (trailing SGR on a full-width line can wrap early in some terms).
	width = max(20, width-2)
	gm := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.TaskList,
			extension.Linkify,
		),
		goldmark.WithRenderer(newMDRenderer(c.st, width)),
	)
	var sb strings.Builder
	if err := gm.Convert([]byte(md), &sb); err != nil {
		return "", err
	}
	return sb.String(), nil
}
