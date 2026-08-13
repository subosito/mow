package mowi

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	xansi "github.com/charmbracelet/x/ansi"
)

// Common fence-language aliases models emit (and filename extensions).
var langAliases = map[string]string{
	"golang":       "go",
	"go-module":    "go",
	"py":           "python",
	"python3":      "python",
	"js":           "javascript",
	"node":         "javascript",
	"ts":           "typescript",
	"tsx":          "tsx",
	"jsx":          "jsx",
	"rs":           "rust",
	"rb":           "ruby",
	"yml":          "yaml",
	"sh":           "bash",
	"shell":        "bash",
	"zsh":          "bash",
	"console":      "bash",
	"shellsession": "bash",
	"text":         "plaintext",
	"plain":        "plaintext",
	"txt":          "plaintext",
	"md":           "markdown",
	"makefile":     "make",
	"dockerfile":   "docker",
	"cs":           "c#",
	"cpp":          "c++",
	"cxx":          "c++",
	"cc":           "c++",
	"h":            "c",
	"hpp":          "c++",
	"kt":           "kotlin",
	"kts":          "kotlin",
	"proto":        "protobuf",
	"tf":           "terraform",
	"hcl":          "terraform",
	"ps1":          "powershell",
	"pwsh":         "powershell",
}

// renderMarkdown renders assistant markdown via the custom goldmark renderer
// (markdown_render.go) with chroma syntax highlighting.
// live=true stabilizes incomplete fences so streaming uses the same pipeline
// as the final frame (avoids plain→markdown flash).
func (m *model) renderMarkdown(md string, width int, live bool) string {
	return renderMarkdownCached(&m.md, md, width, live)
}

// renderMarkdownCached is safe to call from a tea.Cmd goroutine (mdCache is locked).
func renderMarkdownCached(c *mdCache, md string, width int, live bool) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	if live {
		md = stabilizeMarkdown(md)
	}
	// Pre-normalize fences so glamour/chroma get a real language id.
	md = normalizeFences(md)
	out, err := c.render(md, width)
	if err != nil {
		return wordWrap(md, width)
	}
	return out
}

// stabilizeMarkdown closes open constructs so a partial stream still renders
// cleanly through glamour (same path as the finished message).
func stabilizeMarkdown(s string) string {
	if s == "" {
		return s
	}
	// Close an unclosed fenced code block (``` …).
	// Count fence openers that begin a line (optional indent).
	fences := 0
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fences++
		}
	}
	out := s
	if fences%2 == 1 {
		// If the last fence line is only ```lang with no body yet, still close.
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "```"
	}
	return out
}

// renderUser is plain text (user prompts rarely need full markdown chrome).
func (m *model) renderUser(text string, width int) string {
	return wordWrap(text, max(16, width))
}

// normalizeFences rewrites ```lang and ```path/file.ext to chroma-friendly names.
func normalizeFences(md string) string {
	var b strings.Builder
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		trim := strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(trim, "```"); ok {
			lang := parseFenceInfo(rest)
			resolved := resolveLangLabel(lang)
			b.WriteString("```")
			b.WriteString(resolved)
		} else {
			b.WriteString(trim)
		}
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// parseFenceInfo extracts the language/filename token from a fence info string.
// Handles: go | main.go | go title="x" | typescript{...}
func parseFenceInfo(info string) string {
	info = strings.TrimSpace(info)
	if info == "" {
		return ""
	}
	// Drop braced attrs: tsx{...}
	if i := strings.IndexAny(info, "{["); i > 0 {
		info = strings.TrimSpace(info[:i])
	}
	// First whitespace-separated token
	tok := info
	if i := strings.IndexAny(info, " \t"); i >= 0 {
		tok = info[:i]
	}
	// key=value junk
	if strings.Contains(tok, "=") {
		return ""
	}
	return strings.Trim(tok, "`\"'")
}

// resolveLangLabel maps aliases and filenames to a lexer name chroma/glamour know.
func resolveLangLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)

	// Filename or path → extension
	if strings.Contains(raw, "/") || strings.Contains(raw, `\`) || strings.Contains(raw, ".") {
		base := filepath.Base(raw)
		if lex := lexers.Match(base); lex != nil {
			if cfg := lex.Config(); cfg != nil && len(cfg.Aliases) > 0 {
				return cfg.Aliases[0]
			}
			if cfg := lex.Config(); cfg != nil && cfg.Name != "" {
				return strings.ToLower(cfg.Name)
			}
		}
		ext := strings.TrimPrefix(filepath.Ext(base), ".")
		if ext != "" {
			if a, ok := langAliases[strings.ToLower(ext)]; ok {
				return a
			}
			if lexers.Get(ext) != nil {
				return ext
			}
		}
	}

	if a, ok := langAliases[lower]; ok {
		return a
	}
	if lexers.Get(lower) != nil {
		return lower
	}
	return lower
}

func resolveLexer(lang, code string) chroma.Lexer {
	lang = resolveLangLabel(lang)
	var lexer chroma.Lexer
	if lang != "" {
		lexer = lexers.Get(lang)
		if lexer == nil {
			lexer = lexers.Match("file." + lang)
		}
		if lexer == nil {
			// try as filename
			lexer = lexers.Match(lang)
		}
	}
	if lexer == nil && code != "" {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	return chroma.Coalesce(lexer)
}

// diffGutter is the geometry of the line-number gutter for one diff.
//
// The number column used to be a fixed 4 cells with two-space gaps, costing 17
// columns before any code appeared. Inside the transcript's own indent that is
// a large fraction of an 80-column terminal spent on whitespace, and most
// diffs never reach four digits. Sizing the column to the largest line number
// the diff actually mentions gives those columns back to the code.
type diffGutter struct {
	numW int // width of each line-number column
}

// newDiffGutter measures a unified diff and returns the gutter geometry.
//
// Width comes from the hunk headers rather than from counting rendered rows,
// because the numbers shown are the file's, not the diff's: a hunk starting at
// line 1200 needs four cells even if it only shows three lines.
func newDiffGutter(lines []string) diffGutter {
	maxLn := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		oh, nh, ok := parseHunkHeader(line)
		if !ok {
			continue
		}
		for _, end := range []int{oh.start + oh.count, nh.start + nh.count} {
			if end > maxLn {
				maxLn = end
			}
		}
	}
	w := len(strconv.Itoa(maxLn))
	// Two cells minimum: a single-column gutter reads as noise next to the
	// separator, and almost every real file passes line 9 anyway.
	if w < diffNumMinWidth {
		w = diffNumMinWidth
	}
	if w > diffNumMaxWidth {
		w = diffNumMaxWidth
	}
	return diffGutter{numW: w}
}

// blank is an empty number cell (the row this side of the diff does not have).
func (g diffGutter) blank() string { return strings.Repeat(" ", g.numW) }

// num formats a line number into its column.
func (g diffGutter) num(n int) string { return fmt.Sprintf("%*d", g.numW, n) }

// ellipsis is the elided-lines marker, right-aligned like a number.
func (g diffGutter) ellipsis() string {
	if g.numW <= 2 {
		return "··"
	}
	return strings.Repeat(" ", g.numW-2) + "··"
}

// prefix renders the line-number cell, one glyph cell, and the separator.
//
//	 8   │ context
//	 9 − │ removed
//	 9 + │ added
//	10   │ context
//
// One number column, not two. A side-by-side old/new pair spends a whole
// column restating what the row already says: a deletion has no new number and
// an addition has no old one, so half the pair is blank on every changed row.
// Showing the line the row actually refers to says the same thing in half the
// width.
//
// The glyph sits in ONE column right of the number, the same column for both
// directions. Bracketing the number instead — "+" before, "−" after — reads
// well on a single row but puts the two signs in different columns, so the eye
// zigzags down a replace run and the glyph fails at the one job it has:
// signalling direction where colour cannot. A shared column means "−" and "+"
// stack directly above one another on a replace pair.
//
// Colour carries direction for most readers; the glyph is what survives
// NO_COLOR, a colour-blind reading, and a copied-out transcript.
//
// Context rows leave the glyph cell blank, so every row's separator lands in
// the same column and the numbers stay right-aligned against each other.
func (g diffGutter) prefix(n, glyph string) string {
	return fmt.Sprintf("%s %s │ ", n, glyph)
}

// numPrefix is prefix for rows with no change glyph (context, markers).
func (g diffGutter) numPrefix(n string) string {
	return g.prefix(n, " ")
}

// pick chooses the number a row displays: the new side when the row exists
// there, the old side otherwise. Context and additions therefore show the
// current file's numbering, which is what a reader navigates by; a deletion
// shows where the line used to be, because it has nowhere else to point.
func (g diffGutter) pick(oldN, newN string) string {
	if strings.TrimSpace(newN) != "" {
		return newN
	}
	return oldN
}

// width is the total cells the gutter occupies, glyph cells and the separator
// included. Used by layout code that needs to know where the body starts.
func (g diffGutter) width() int {
	// num + " " + glyph + " " + "│" + " "
	return g.numW + 1 + 1 + 1 + 1 + 1
}

const (
	// diffNumMinWidth keeps a one-digit file from rendering a cramped gutter.
	diffNumMinWidth = 2
	// diffNumMaxWidth caps the column so a generated file with a six-digit
	// line count cannot eat the body. Numbers past this overflow their cell
	// and push the row right, which is still better than reserving the space
	// on every diff.
	diffNumMaxWidth = 5
)

// colorDiffLines paints a unified diff for permission previews / legacy callers.
func colorDiffLines(th theme, code string) string {
	return renderPrettyDiff(th, code, 0)
}

// renderPrettyDiff lives in diff_render.go: it parses unified text into a
// structured model (diff_model.go) and paints unified (or split) review rows.
// True side-by-side panes stay off the compact transcript card; the expanded
// overlay may offer split when the terminal is wide enough.

// Add/deleted rows carry a glyph so the change direction survives a
// color-blind (or no-color) terminal instead of being signalled by tint alone.
//
// The glyph sits in a fixed column just left of the body, NOT in the
// line-number cell the row happens to leave empty. Right-aligning the sign
// inside the 4-cell number columns put "+" six columns away from "−", so the
// eye zigzagged down a replace pair and direction was not scannable — the one
// job the glyph exists to do.
const (
	diffSignAdd = "+"
	diffSignDel = "\u2212"
	diffSignCtx = " "
)

// formatDiffRow builds "  old  new │ S body" with a tinted body (add/del).
func formatDiffRow(th theme, g diffGutter, bodyStyle lipgloss.Style, oldN, newN, sign, body string, width int) string {
	if body == "" {
		body = " "
	}
	return formatDiffRowPre(th, g, bodyStyle, oldN, newN, sign, bodyStyle.Render(body), width)
}

// diffNumTint is the line-number style for a changed row: the row's own accent
// colour on no background.
//
// The gutter deliberately carries no wash. Banding it too made a changed block
// one rectangle, but it forced the numbers to compete with a mid-tone
// background — which is what drove a whole chain of contrast machinery (seat
// the band so an ink is legible, pick an ink that is legible but not stark).
// Tinting the digits instead says the same thing with none of that: green
// numbers mean an inserted line, red mean a removed one, and the band is left
// to mark the content that actually changed.
func diffNumTint(th theme, bodyStyle lipgloss.Style) lipgloss.Style {
	fg := bodyStyle.GetForeground()
	if fg == nil {
		return th.DiffNum
	}
	// Faint off: terminals implement it inconsistently, and a tinted number is
	// already quiet enough without it.
	return lipgloss.NewStyle().Foreground(fg).Faint(false)
}

// formatDiffRowPre is formatDiffRow for a body that is already styled (word
// diff spans), so the caller's per-word emphasis is not flattened by a single
// Render over the whole line.
//
// The band covers the body only; the line number is tinted rather than washed,
// so the eye reads "which lines" from colour and "what changed" from the
// block. Padding is skipped when width is unknown (colorDiffLines passes 0):
// there is no column to pad to.
func formatDiffRowPre(th theme, g diffGutter, bodyStyle lipgloss.Style, oldN, newN, sign, styledBody string, width int) string {
	numStyle := diffNumTint(th, bodyStyle)
	gutter := numStyle.Render(g.prefix(g.pick(oldN, newN), sign))
	if width > 0 {
		if pad := width - lipgloss.Width(gutter) - lipgloss.Width(styledBody); pad > 0 {
			styledBody += bodyStyle.Render(strings.Repeat(" ", pad))
		}
	}
	return clipDiffRow(gutter+styledBody, width)
}

// diffSeg is one intraline span for word-aware emphasis on a paired replace.
type diffSeg struct {
	text    string
	changed bool
}

// emphasizeWordDiff styles a replaced pair so changed tokens stand out and
// shared tokens recede (flashdiff-style chips + soft band ink).
//
// Tokenisation is whitespace/punctuation/identifier aware (not space-only),
// and pairing uses LCS so a mid-line edit does not force a full rewrite paint.
// Falls back to whole-line tint when nothing is shared or under NO_COLOR.
func emphasizeWordDiff(th theme, oldBody, newBody string) (oldText, newText string) {
	plain := func() (string, string) {
		o, n := oldBody, newBody
		if o == "" {
			o = " "
		}
		if n == "" {
			n = " "
		}
		return th.DiffDel.Render(o), th.DiffAdd.Render(n)
	}
	if oldBody == "" || newBody == "" {
		return plain()
	}
	// Under NO_COLOR chips/SGR would still emit; keep plain text only.
	if noColor() {
		return plain()
	}

	oldSegs, newSegs, ok := wordDiffSegs(oldBody, newBody)
	if !ok {
		return plain()
	}
	return paintDiffBodySegs(th.DiffDelSoft, th.DiffWordDel, oldSegs, 0),
		paintDiffBodySegs(th.DiffAddSoft, th.DiffWordAdd, newSegs, 0)
}

// wordDiffSegs tokenises both sides and pairs them with LCS. ok is false when
// the lines share nothing useful (full rewrite) or are identical.
func wordDiffSegs(oldBody, newBody string) (oldSegs, newSegs []diffSeg, ok bool) {
	if oldBody == newBody {
		return nil, nil, false
	}
	ot, nt := splitDiffTokens(oldBody), splitDiffTokens(newBody)
	if len(ot) == 0 || len(nt) == 0 {
		return nil, nil, false
	}
	oldSegs, newSegs = tokenLCSSegs(ot, nt)
	var substantive, changed int
	count := func(segs []diffSeg) {
		for _, s := range segs {
			if s.changed {
				changed++
				continue
			}
			// Whitespace-only equals do not count as structure; "alpha beta" vs
			// "gamma delta" shares a space but is still a full rewrite.
			if strings.TrimSpace(s.text) != "" {
				substantive++
			}
		}
	}
	count(oldSegs)
	count(newSegs)
	// No substantive shared tokens → whole-line tint is clearer than chips.
	if substantive == 0 {
		return nil, nil, false
	}
	// Nothing actually marked changed (shouldn't happen when texts differ).
	if changed == 0 {
		return nil, nil, false
	}
	return oldSegs, newSegs, true
}

// splitDiffTokens splits a line into lossless tokens: whitespace runs,
// identifier runs (letter/digit/_/Unicode letter or number), and single
// punctuation/symbol runes. Joining reproduces the line exactly.
//
// Space-only splitting (flashdiff) treats "foo(x)" as one token; punctuation
// awareness lets "foo(x)" vs "foo(y)" chip only the argument.
func splitDiffTokens(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	// kind: 0 none, 1 space, 2 word, 3 other (flush each other rune alone)
	kind := 0
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		kind = 0
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if kind != 1 {
				flush()
				kind = 1
			}
			cur.WriteRune(r)
		case isDiffWordRune(r):
			if kind != 2 {
				flush()
				kind = 2
			}
			cur.WriteRune(r)
		default:
			// Each punctuation/symbol is its own token so ", " stays
			// separable from identifiers and edits land on the right glyph.
			flush()
			out = append(out, string(r))
		}
	}
	flush()
	return out
}

// splitDiffWords is the historical space/tab splitter. Kept as a thin alias
// for older tests that assert whitespace-preserving splits; new paint paths
// use splitDiffTokens.
func splitDiffWords(s string) []string {
	return splitDiffTokens(s)
}

func isDiffWordRune(r rune) bool {
	if r == '_' {
		return true
	}
	// ASCII fast path, then unicode for identifiers like café / α1.
	if r <= 0x7f {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// tokenLCSSegs pairs two token slices via LCS and merges adjacent equal-kind
// spans. Pure stdlib; lines are short so O(n·m) is fine.
func tokenLCSSegs(a, b []string) (as, bs []diffSeg) {
	n, m := len(a), len(b)
	// dp[i][j] = LCS length of a[i:] and b[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			as = append(as, diffSeg{text: a[i]})
			bs = append(bs, diffSeg{text: b[j]})
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			as = append(as, diffSeg{text: a[i], changed: true})
			i++
		} else {
			bs = append(bs, diffSeg{text: b[j], changed: true})
			j++
		}
	}
	for ; i < n; i++ {
		as = append(as, diffSeg{text: a[i], changed: true})
	}
	for ; j < m; j++ {
		bs = append(bs, diffSeg{text: b[j], changed: true})
	}
	return mergeDiffSegs(as), mergeDiffSegs(bs)
}

func mergeDiffSegs(in []diffSeg) []diffSeg {
	if len(in) == 0 {
		return nil
	}
	out := make([]diffSeg, 0, len(in))
	for _, s := range in {
		if s.text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].changed == s.changed {
			out[n-1].text += s.text
			continue
		}
		out = append(out, s)
	}
	return out
}

// countDiffStats tallies +/− lines in a unified diff body (ignores headers).
func countDiffStats(code string) (add, del int) {
	for _, line := range strings.Split(code, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			add++
		} else if strings.HasPrefix(line, "-") {
			del++
		}
	}
	return add, del
}

// hunkRange is one side of a unified @@ header.
type hunkRange struct {
	start, count int
}

func parseHunkHeader(line string) (oldH, newH hunkRange, ok bool) {
	// @@ -l,s +l,s @@ optional trailing context
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "@@") {
		return hunkRange{}, hunkRange{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
	if i := strings.Index(rest, "@@"); i >= 0 {
		rest = strings.TrimSpace(rest[:i])
	}
	parts := strings.Fields(rest)
	if len(parts) < 2 {
		return hunkRange{}, hunkRange{}, false
	}
	oldH, ok1 := parseHunkSpec(parts[0])
	newH, ok2 := parseHunkSpec(parts[1])
	return oldH, newH, ok1 && ok2
}

func parseHunkSpec(s string) (hunkRange, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return hunkRange{}, false
	}
	sign := s[0]
	if sign != '-' && sign != '+' {
		return hunkRange{}, false
	}
	body := s[1:]
	start, count := 0, 1
	if i := strings.IndexByte(body, ','); i >= 0 {
		fmt.Sscanf(body[:i], "%d", &start)
		fmt.Sscanf(body[i+1:], "%d", &count)
	} else {
		fmt.Sscanf(body, "%d", &start)
		if start == 0 {
			count = 0
		}
	}
	return hunkRange{start: start, count: count}, true
}

// formatHunkReviewLabel is human review language, not git "−a → +b".
func formatHunkReviewLabel(oldH, newH hunkRange) string {
	// Pure create
	if oldH.count == 0 && newH.count > 0 {
		if newH.count == 1 {
			return "new line"
		}
		return fmt.Sprintf("new file · %d lines", newH.count)
	}
	// Pure delete
	if newH.count == 0 && oldH.count > 0 {
		if oldH.count == 1 {
			return "deleted line"
		}
		return fmt.Sprintf("removed · %d lines", oldH.count)
	}
	// Show the side that actually spans the change. Always reporting the new
	// side made a delete-heavy hunk (@@ -1,4 +1,1 @@) read as "lines 1",
	// naming one surviving line while hiding the three that went away.
	rng := formatSide(newH)
	switch {
	case newH.count <= 0:
		rng = formatSide(oldH)
	case oldH.count > newH.count:
		rng = formatSide(oldH)
	}
	return "lines " + rng
}

func formatSide(h hunkRange) string {
	if h.count <= 0 {
		return fmt.Sprintf("%d", h.start)
	}
	if h.count == 1 {
		return fmt.Sprintf("%d", h.start)
	}
	end := h.start + h.count - 1
	if h.start == 0 {
		return fmt.Sprintf("1–%d", h.count)
	}
	return fmt.Sprintf("%d–%d", h.start, end)
}

func clipDiffRow(row string, width int) string {
	if width <= 0 || lipgloss.Width(row) <= width {
		return row
	}
	return xansi.Truncate(row, width, "…")
}

// wordWrap wraps by display width (ANSI-aware, CJK/emoji safe). The old
// byte-indexed version split UTF-8 runes mid-sequence and wrapped wide glyphs
// at a third of the terminal.
func wordWrap(s string, width int) string {
	if width < 8 {
		return s
	}
	return xansi.Wrap(s, width, "")
}

// truncate flattens newlines and clamps to display width (rune-safe).
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if xansi.StringWidth(s) <= n {
		return s
	}
	return xansi.Truncate(s, n, "…")
}

// short clamps to display width (rune-safe).
func short(s string, n int) string {
	if xansi.StringWidth(s) <= n {
		return s
	}
	return xansi.Truncate(s, n, "…")
}

// sanitizeDisplay strips terminal control sequences from untrusted text
// (model output, tool results) before it is stored or painted. Removes
// ESC-initiated sequences (CSI/OSC/DCS/APC/…) and control runes except \n
// and \t — otherwise a crafted reply could retitle the terminal, repaint the
// permission prompt, or set the clipboard via OSC 52.
func sanitizeDisplay(s string) string {
	if s == "" {
		return s
	}
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = xansi.Strip(s)
	}
	if strings.IndexFunc(s, isBadControlRune) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isBadControlRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isBadControlRune: C0 except \n\t, DEL, and C1 (covers lone CSI/OSC runes).
func isBadControlRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
