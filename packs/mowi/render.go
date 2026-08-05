package mowi

import (
	"fmt"
	"path/filepath"
	"strings"

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

// colorDiffLines paints a unified diff for permission previews / legacy callers.
func colorDiffLines(th theme, code string) string {
	return renderPrettyDiff(th, code, 0)
}

// renderPrettyDiff formats unified-diff body as a code-review panel:
//
//	lines 10–14 · +2 −1          soft hunk header (not raw @@)
//	10  10 │ context
//	11     │ removed             red tint, no leading "-"
//	    12 │ added               green tint, no leading "+"
//
// Dual line numbers (old | new) match GitHub-style review UIs. Meta ---/+++
// headers are omitted (path lives on the entry title).
//
// Replaced lines are paired: a −/+ run of equal length is emitted as adjacent
// old/new rows (first removal, first addition, second removal, …) instead of
// every deletion followed by every addition. Within a pair the words that
// actually differ are emphasised, so a one-token edit does not read as two
// entirely rewritten lines. True side-by-side panes are deliberately not used:
// at 80 columns each pane would get ~28 cells, which wraps real code to
// uselessness inside the transcript's indent.
func renderPrettyDiff(th theme, code string, width int) string {
	code = strings.TrimRight(code, "\n")
	if code == "" {
		return ""
	}
	var b strings.Builder
	oldLn, newLn := 1, 1
	haveNums := false
	first := true
	// Pending removals held back so an immediately following addition run can
	// be paired with them line-for-line.
	var pendingDel []string

	nl := func() {
		if !first {
			b.WriteByte('\n')
		}
		first = false
	}
	// flushDel emits held removals unpaired (a pure deletion run).
	flushDel := func() {
		for _, body := range pendingDel {
			on, nn := "    ", diffSignDel
			if haveNums {
				on = fmt.Sprintf("%4d", oldLn)
				oldLn++
			}
			nl()
			b.WriteString(formatDiffRow(th, th.DiffDel, on, nn, body, width))
		}
		pendingDel = nil
	}

	lines := strings.Split(code, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			flushDel()
			oh, nh, ok := parseHunkHeader(line)
			if ok {
				oldLn, newLn = oh.start, nh.start
				if oldLn == 0 && oh.count == 0 {
					oldLn = 0
				}
				if newLn == 0 && nh.count == 0 {
					newLn = 0
				}
				haveNums = true
				nl()
				b.WriteString(th.DiffMeta.Render("  " + formatHunkReviewLabel(oh, nh)))
			} else if strings.TrimSpace(line) == "@@" {
				oldLn, newLn = 1, 1
				haveNums = true
				nl()
				b.WriteString(th.DiffMeta.Render("  change"))
			} else {
				nl()
				b.WriteString(th.DiffMeta.Render("  " + strings.TrimSpace(line)))
			}
			continue
		}

		switch {
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			// Hold: the next run may be its replacement.
			pendingDel = append(pendingDel, strings.TrimPrefix(line, "-"))

		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			body := strings.TrimPrefix(line, "+")
			if len(pendingDel) > 0 {
				// Paired replace: old row then its new row, so the change reads
				// as one edit rather than two unrelated blocks.
				oldBody := pendingDel[0]
				pendingDel = pendingDel[1:]
				oldText, newText := emphasizeWordDiff(th, oldBody, body)

				on, nn := "    ", diffSignDel
				if haveNums {
					on = fmt.Sprintf("%4d", oldLn)
					oldLn++
				}
				nl()
				b.WriteString(formatDiffRowPre(th, on, nn, oldText, width))

				on, nn = diffSignAdd, "    "
				if haveNums {
					nn = fmt.Sprintf("%4d", newLn)
					newLn++
				}
				nl()
				b.WriteString(formatDiffRowPre(th, on, nn, newText, width))
				continue
			}
			on, nn := diffSignAdd, "    "
			if haveNums {
				nn = fmt.Sprintf("%4d", newLn)
				newLn++
			}
			nl()
			b.WriteString(formatDiffRow(th, th.DiffAdd, on, nn, body, width))

		case strings.HasPrefix(line, "\\"): // "\ No newline at end of file"
			flushDel()
			nl()
			b.WriteString(th.Muted.Render(fmt.Sprintf("  %s  %s │ %s", "    ", "    ", "no newline at end of file")))
		case strings.HasPrefix(line, "…"):
			flushDel()
			nl()
			b.WriteString(th.Muted.Render(fmt.Sprintf("  %s  %s │ %s", "  ··", "  ··", strings.TrimSpace(line))))
		default:
			flushDel()
			body := line
			if strings.HasPrefix(body, " ") {
				body = body[1:]
			}
			on, nn := "    ", "    "
			if haveNums {
				on = fmt.Sprintf("%4d", oldLn)
				nn = fmt.Sprintf("%4d", newLn)
				oldLn++
				newLn++
			}
			// Context: muted numbers, normal text — no tint.
			nl()
			gutter := th.DiffNum.Render(fmt.Sprintf("  %s  %s │ ", on, nn))
			b.WriteString(clipDiffRow(gutter+th.DiffCtx.Render(body), width))
		}
	}
	flushDel()
	return b.String()
}

// Add/deleted rows carry a glyph in the line-number column they do not own, so
// the change direction survives a color-blind (or no-color) terminal instead of
// being signalled by the row tint alone.
const (
	diffSignAdd = "   +"
	diffSignDel = "   \u2212"
)

// formatDiffRow builds "  old  new │ body" with tinted body (add/del).
func formatDiffRow(th theme, bodyStyle lipgloss.Style, oldN, newN, body string, width int) string {
	if body == "" {
		body = " "
	}
	return formatDiffRowPre(th, oldN, newN, bodyStyle.Render(body), width)
}

// formatDiffRowPre is formatDiffRow for a body that is already styled (word
// diff spans), so the caller's per-word emphasis is not flattened by a single
// Render over the whole line.
func formatDiffRowPre(th theme, oldN, newN, styledBody string, width int) string {
	gutter := th.DiffNum.Render(fmt.Sprintf("  %s  %s │ ", oldN, newN))
	return clipDiffRow(gutter+styledBody, width)
}

// emphasizeWordDiff styles a replaced pair so the changed words stand out from
// the words both lines share.
//
// A one-token edit ("timeout 30" → "timeout 60") otherwise paints two fully
// tinted lines and the reader has to diff them by eye. Shared prefix/suffix
// words render in the row's base tint; the differing middle is bold, so the
// actual edit is findable at a glance.
//
// Falls back to plain whole-line tint when the lines share nothing (a genuine
// rewrite), where per-word emphasis would just add noise.
func emphasizeWordDiff(th theme, oldBody, newBody string) (oldText, newText string) {
	delStyle, addStyle := th.DiffDel, th.DiffAdd
	plain := func() (string, string) {
		o, n := oldBody, newBody
		if o == "" {
			o = " "
		}
		if n == "" {
			n = " "
		}
		return delStyle.Render(o), addStyle.Render(n)
	}
	if oldBody == "" || newBody == "" {
		return plain()
	}

	oldWords, newWords := splitDiffWords(oldBody), splitDiffWords(newBody)
	// Common prefix, then common suffix over what remains.
	pre := 0
	for pre < len(oldWords) && pre < len(newWords) && oldWords[pre] == newWords[pre] {
		pre++
	}
	suf := 0
	for suf < len(oldWords)-pre && suf < len(newWords)-pre &&
		oldWords[len(oldWords)-1-suf] == newWords[len(newWords)-1-suf] {
		suf++
	}
	// Nothing shared, or everything shared: whole-line tint is clearer.
	if pre == 0 && suf == 0 {
		return plain()
	}
	if pre == len(oldWords) && pre == len(newWords) {
		return plain()
	}

	build := func(words []string, base lipgloss.Style) string {
		mid := base.Bold(true)
		var b strings.Builder
		for i, w := range words {
			if i < pre || i >= len(words)-suf {
				b.WriteString(base.Render(w))
				continue
			}
			b.WriteString(mid.Render(w))
		}
		return b.String()
	}
	return build(oldWords, delStyle), build(newWords, addStyle)
}

// splitDiffWords splits a line into words that keep their trailing whitespace,
// so joining the pieces reproduces the line exactly (indentation included).
func splitDiffWords(s string) []string {
	var out []string
	var cur strings.Builder
	inSpace := false
	for _, r := range s {
		isSpace := r == ' ' || r == '\t'
		if isSpace {
			inSpace = true
			cur.WriteRune(r)
			continue
		}
		if inSpace {
			out = append(out, cur.String())
			cur.Reset()
			inSpace = false
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
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
	// Range on the new side when possible (where the reader lands).
	rng := formatSide(newH)
	if newH.count <= 0 {
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
