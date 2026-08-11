package mowi

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// diffViewMode selects how a parsed model is painted.
type diffViewMode int

const (
	diffModeUnified diffViewMode = iota
	diffModeSplit
)

func (m diffViewMode) String() string {
	if m == diffModeSplit {
		return "split"
	}
	return "unified"
}

// diffPaintOpts controls optional path-aware syntax and layout mode.
type diffPaintOpts struct {
	Path   string
	Mode   diffViewMode
	Width  int
	Syntax bool // when true and Path set, apply restrained chroma FGs
}

// renderPrettyDiff formats unified-diff body as a code-review panel.
// Retains the historical signature used by tests and permission previews.
func renderPrettyDiff(th theme, code string, width int) string {
	return renderDiffModel(th, parseUnifiedDiff(code), diffPaintOpts{Width: width, Mode: diffModeUnified, Syntax: true})
}

// renderPrettyDiffPath is renderPrettyDiff with a file path for lexer selection.
func renderPrettyDiffPath(th theme, code, path string, width int) string {
	d := parseUnifiedDiff(code)
	if path != "" {
		d.Path = path
	}
	return renderDiffModel(th, d, diffPaintOpts{Path: d.Path, Width: width, Mode: diffModeUnified, Syntax: true})
}

// renderDiffModel paints a parsed model in unified or split layout.
func renderDiffModel(th theme, d diffModel, opt diffPaintOpts) string {
	if len(d.Lines) == 0 {
		return ""
	}
	path := opt.Path
	if path == "" {
		path = d.Path
	}
	opt.Path = path

	if opt.Mode == diffModeSplit {
		if out, ok := renderDiffSplit(th, d, opt); ok {
			return out
		}
		// Narrow terminal: graceful unified fallback.
		opt.Mode = diffModeUnified
	}
	return renderDiffUnified(th, d, opt)
}

// renderDiffUnified is the compact review panel used by the transcript card
// and the overlay's unified mode. Behaviour matches the historical
// renderPrettyDiff: block del-then-add runs, word emphasis on equal-length
// replace mids, shared sign column, soft hunk labels.
func renderDiffUnified(th theme, d diffModel, opt diffPaintOpts) string {
	width := opt.Width
	// Measure gutter from hunk headers in the model.
	rawLines := make([]string, 0, len(d.Lines))
	for _, ln := range d.Lines {
		if ln.Op == dOpHunk {
			rawLines = append(rawLines, ln.Text)
		}
	}
	g := newDiffGutter(rawLines)
	if len(rawLines) == 0 {
		// No hunk headers: still need a minimal gutter for notes/context.
		g = diffGutter{numW: diffNumMinWidth}
	}

	var hl *diffHighlighter
	if opt.Syntax && opt.Path != "" && !noColor() {
		hl = newDiffHighlighter(th)
	}

	var b strings.Builder
	first := true
	nl := func() {
		if !first {
			b.WriteByte('\n')
		}
		first = false
	}

	var dels, adds []diffModelLine
	flushRun := func() {
		if len(dels) == 0 && len(adds) == 0 {
			return
		}
		// Leading identical del/add pairs collapse to context (shared text).
		pre := 0
		for pre < len(dels) && pre < len(adds) && dels[pre].Text == adds[pre].Text {
			on, nn := g.blank(), g.blank()
			if dels[pre].OldNum > 0 {
				on = g.num(dels[pre].OldNum)
			}
			if adds[pre].NewNum > 0 {
				nn = g.num(adds[pre].NewNum)
			}
			nl()
			body := expandDiffTabs(dels[pre].Text, 4)
			gutter := th.DiffNum.Render(g.numPrefix(g.pick(on, nn)))
			styled := paintDiffBody(th, hl, opt.Path, body, th.DiffCtx, 0)
			b.WriteString(clipDiffRow(gutter+styled, width))
			pre++
		}

		remDels, remAdds := dels[pre:], adds[pre:]
		suf := 0
		for suf < len(remDels) && suf < len(remAdds) &&
			remDels[len(remDels)-1-suf].Text == remAdds[len(remAdds)-1-suf].Text {
			suf++
		}
		midDels := remDels[:len(remDels)-suf]
		midAdds := remAdds[:len(remAdds)-suf]
		sufDels := remDels[len(remDels)-suf:]

		var oldStyled, newStyled []string
		if len(midDels) > 0 && len(midDels) == len(midAdds) {
			oldStyled = make([]string, len(midDels))
			newStyled = make([]string, len(midAdds))
			for i := range midDels {
				oldStyled[i], newStyled[i] = emphasizeWordDiff(th,
					expandDiffTabs(midDels[i].Text, 4),
					expandDiffTabs(midAdds[i].Text, 4))
			}
		}

		for i, ml := range midDels {
			on, nn := g.blank(), g.blank()
			if ml.OldNum > 0 {
				on = g.num(ml.OldNum)
			}
			nl()
			if oldStyled != nil {
				b.WriteString(formatDiffRowPre(th, g, th.DiffDel, on, nn, diffSignDel, oldStyled[i], width))
			} else {
				body := expandDiffTabs(ml.Text, 4)
				// Skip chroma when word-diff isn't used but we still want
				// restrained FGs under the del band when available.
				if hl != nil {
					styled := paintDiffBody(th, hl, opt.Path, body, th.DiffDel, 0)
					b.WriteString(formatDiffRowPre(th, g, th.DiffDel, on, nn, diffSignDel, styled, width))
				} else {
					b.WriteString(formatDiffRow(th, g, th.DiffDel, on, nn, diffSignDel, body, width))
				}
			}
		}
		for i, ml := range midAdds {
			on, nn := g.blank(), g.blank()
			if ml.NewNum > 0 {
				nn = g.num(ml.NewNum)
			}
			nl()
			if newStyled != nil {
				b.WriteString(formatDiffRowPre(th, g, th.DiffAdd, on, nn, diffSignAdd, newStyled[i], width))
			} else {
				body := expandDiffTabs(ml.Text, 4)
				if hl != nil {
					styled := paintDiffBody(th, hl, opt.Path, body, th.DiffAdd, 0)
					b.WriteString(formatDiffRowPre(th, g, th.DiffAdd, on, nn, diffSignAdd, styled, width))
				} else {
					b.WriteString(formatDiffRow(th, g, th.DiffAdd, on, nn, diffSignAdd, body, width))
				}
			}
		}
		for i := 0; i < suf; i++ {
			on, nn := g.blank(), g.blank()
			if sufDels[i].OldNum > 0 {
				on = g.num(sufDels[i].OldNum)
			}
			// Prefer the paired add's new number when present.
			if pre+len(midDels)+i < len(adds) && adds[pre+len(midDels)+i].NewNum > 0 {
				nn = g.num(adds[pre+len(midDels)+i].NewNum)
			} else if sufDels[i].NewNum > 0 {
				nn = g.num(sufDels[i].NewNum)
			}
			nl()
			body := expandDiffTabs(sufDels[i].Text, 4)
			gutter := th.DiffNum.Render(g.numPrefix(g.pick(on, nn)))
			styled := paintDiffBody(th, hl, opt.Path, body, th.DiffCtx, 0)
			b.WriteString(clipDiffRow(gutter+styled, width))
		}
		dels, adds = nil, nil
	}

	for _, ml := range d.Lines {
		switch ml.Op {
		case dOpHunk:
			flushRun()
			nl()
			if ml.HunkOK {
				b.WriteString(th.DiffMeta.Render("  " + formatHunkReviewLabel(ml.OldH, ml.NewH)))
			} else if ml.Text == "@@" {
				b.WriteString(th.DiffMeta.Render("  change"))
			} else {
				b.WriteString(th.DiffMeta.Render("  " + ml.Text))
			}
		case dOpDel:
			dels = append(dels, ml)
		case dOpAdd:
			adds = append(adds, ml)
		case dOpNote:
			flushRun()
			nl()
			if strings.HasPrefix(ml.Text, "…") || strings.HasPrefix(ml.Text, "...") {
				b.WriteString(th.Muted.Render(g.numPrefix(g.ellipsis()) + ml.Text))
			} else {
				b.WriteString(th.Muted.Render(g.numPrefix(g.blank()) + ml.Text))
			}
		default: // context
			flushRun()
			on, nn := g.blank(), g.blank()
			if ml.OldNum > 0 {
				on = g.num(ml.OldNum)
			}
			if ml.NewNum > 0 {
				nn = g.num(ml.NewNum)
			}
			nl()
			body := expandDiffTabs(ml.Text, 4)
			gutter := th.DiffNum.Render(g.numPrefix(g.pick(on, nn)))
			styled := paintDiffBody(th, hl, opt.Path, body, th.DiffCtx, 0)
			b.WriteString(clipDiffRow(gutter+styled, width))
		}
	}
	flushRun()
	return b.String()
}

// splitPair is one side-by-side row: left = old, right = new.
// Either side may be empty (blank cell) for unequal add/delete runs.
type splitPair struct {
	Left, Right *diffModelLine
}

// buildSplitPairs zips del/add runs into side-by-side rows; context is mirrored.
func buildSplitPairs(lines []diffModelLine) []splitPair {
	var out []splitPair
	var dels, adds []diffModelLine
	flush := func() {
		n := len(dels)
		if len(adds) > n {
			n = len(adds)
		}
		for i := 0; i < n; i++ {
			var l, r *diffModelLine
			if i < len(dels) {
				d := dels[i]
				l = &d
			}
			if i < len(adds) {
				a := adds[i]
				r = &a
			}
			out = append(out, splitPair{Left: l, Right: r})
		}
		dels, adds = nil, nil
	}
	for i := range lines {
		ml := lines[i]
		switch ml.Op {
		case dOpDel:
			dels = append(dels, ml)
		case dOpAdd:
			adds = append(adds, ml)
		case dOpHunk, dOpNote:
			flush()
			// Hunk/note spans full width via a single left cell; right nil.
			cp := ml
			out = append(out, splitPair{Left: &cp, Right: nil})
		default:
			flush()
			cp := ml
			out = append(out, splitPair{Left: &cp, Right: &cp})
		}
	}
	flush()
	return out
}

// renderDiffSplit paints side-by-side panes. ok=false means the caller should
// fall back to unified (terminal too narrow for useful columns).
func renderDiffSplit(th theme, d diffModel, opt diffPaintOpts) (string, bool) {
	width := opt.Width
	if width < splitDiffMinWidth {
		return "", false
	}
	// Divider (1) + two columns.
	colW := (width - 1) / 2
	if colW < splitColMinWidth {
		return "", false
	}

	rawLines := make([]string, 0, 8)
	for _, ln := range d.Lines {
		if ln.Op == dOpHunk {
			rawLines = append(rawLines, ln.Text)
		}
	}
	g := newDiffGutter(rawLines)
	if g.numW < diffNumMinWidth {
		g.numW = diffNumMinWidth
	}
	// Per-side gutter uses one number column (old on left, new on right).
	// Total: numW + " " + glyph + " " + "│" + " " = numW+5, matching g.width().
	gutterW := g.width()
	bodyW := colW - gutterW
	if bodyW < 8 {
		return "", false
	}

	var hl *diffHighlighter
	if opt.Syntax && opt.Path != "" && !noColor() {
		hl = newDiffHighlighter(th)
	}

	pairs := buildSplitPairs(d.Lines)
	div := th.Muted.Faint(true).Render("│")
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('\n')
		}
		// Full-width meta/hunk rows.
		if p.Left != nil && (p.Left.Op == dOpHunk || p.Left.Op == dOpNote) && p.Right == nil {
			b.WriteString(renderSplitMeta(th, g, *p.Left, width))
			continue
		}
		left := renderSplitCell(th, hl, opt.Path, g, p.Left, colW, bodyW, true)
		right := renderSplitCell(th, hl, opt.Path, g, p.Right, colW, bodyW, false)
		// Pad columns to colW in display cells so the divider stays vertical.
		left = padDiffCell(left, colW)
		right = padDiffCell(right, colW)
		b.WriteString(left)
		b.WriteString(div)
		b.WriteString(right)
	}
	return b.String(), true
}

func renderSplitMeta(th theme, g diffGutter, ml diffModelLine, width int) string {
	switch ml.Op {
	case dOpHunk:
		var label string
		if ml.HunkOK {
			label = formatHunkReviewLabel(ml.OldH, ml.NewH)
		} else if ml.Text == "@@" {
			label = "change"
		} else {
			label = ml.Text
		}
		return clipDiffRow(th.DiffMeta.Render("  "+label), width)
	default:
		return clipDiffRow(th.Muted.Render(g.numPrefix(g.blank())+ml.Text), width)
	}
}

func renderSplitCell(th theme, hl *diffHighlighter, path string, g diffGutter, ml *diffModelLine, colW, bodyW int, left bool) string {
	if ml == nil {
		return strings.Repeat(" ", colW)
	}
	body := expandDiffTabs(ml.Text, 4)
	var num, sign string
	var style lipgloss.Style
	switch ml.Op {
	case dOpDel:
		style = th.DiffDel
		sign = diffSignDel
		if ml.OldNum > 0 {
			num = g.num(ml.OldNum)
		} else {
			num = g.blank()
		}
	case dOpAdd:
		style = th.DiffAdd
		sign = diffSignAdd
		if ml.NewNum > 0 {
			num = g.num(ml.NewNum)
		} else {
			num = g.blank()
		}
	default:
		style = th.DiffCtx
		sign = " "
		if left {
			if ml.OldNum > 0 {
				num = g.num(ml.OldNum)
			} else if ml.NewNum > 0 {
				num = g.num(ml.NewNum)
			} else {
				num = g.blank()
			}
		} else {
			if ml.NewNum > 0 {
				num = g.num(ml.NewNum)
			} else if ml.OldNum > 0 {
				num = g.num(ml.OldNum)
			} else {
				num = g.blank()
			}
		}
	}
	// Truncate body to bodyW display cells before styling.
	if xansi.StringWidth(body) > bodyW {
		body = xansi.Truncate(body, bodyW, "…")
	}
	var styled string
	if ml.Op == dOpAdd || ml.Op == dOpDel {
		numStyle := diffNumTint(th, style)
		gutter := numStyle.Render(g.prefix(num, sign))
		if hl != nil {
			styled = paintDiffBody(th, hl, path, body, style, bodyW)
		} else {
			if body == "" {
				body = " "
			}
			styled = style.Render(body)
			if pad := bodyW - lipgloss.Width(styled); pad > 0 {
				styled += style.Render(strings.Repeat(" ", pad))
			}
		}
		return clipDiffRow(gutter+styled, colW)
	}
	// Context: muted numbers, optional syntax.
	gutter := th.DiffNum.Render(g.numPrefix(num))
	if hl != nil {
		styled = paintDiffBody(th, hl, path, body, style, bodyW)
	} else {
		if body == "" {
			body = " "
		}
		styled = style.Render(body)
	}
	return clipDiffRow(gutter+styled, colW)
}

// padDiffCell right-pads s to width display cells (ANSI-aware).
func padDiffCell(s string, width int) string {
	if width <= 0 {
		return s
	}
	s = xansi.Truncate(s, width, "")
	for xansi.StringWidth(s) < width {
		s += " "
	}
	return s
}

// splitModeAvailable reports whether width can host a useful split view.
func splitModeAvailable(width int) bool {
	if width < splitDiffMinWidth {
		return false
	}
	colW := (width - 1) / 2
	return colW >= splitColMinWidth
}

// formatDiffOverlayTitle is the chrome line above the overlay body.
func formatDiffOverlayTitle(th theme, op, path string, add, del int, mode diffViewMode, width int, canSplit bool) string {
	base := diffBasename(path)
	if base == "" {
		base = "diff"
	}
	var verb string
	switch op {
	case "created":
		verb = th.DiffAdd.UnsetBackground().Render("created")
	case "wrote":
		verb = th.DiffMeta.Render("wrote")
	case "edited":
		verb = th.DiffMeta.Render("edited")
	default:
		if op != "" {
			verb = th.DiffMeta.Render(op)
		} else {
			verb = th.DiffMeta.Render("diff")
		}
	}
	name := th.Accent.Render(base)
	stats := formatDiffStats(th, add, del, op)
	left := verb + "  " + name
	if stats != "" {
		left += "  " + stats
	}

	modeLabel := mode.String()
	var right string
	if canSplit {
		right = th.Muted.Render(modeLabel + " · tab toggle · esc close")
	} else {
		right = th.Muted.Render(modeLabel + " · esc close")
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Drop the right cluster if the title is too wide.
		return xansi.Truncate(left, width, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// collapseDiffBody keeps the first maxLines of a unified body and appends a
// fold marker with remaining +/− stats. Used by the compact transcript card.
func collapseDiffBody(body string, maxLines int) string {
	if maxLines < 1 || body == "" {
		return body
	}
	bodyLines := strings.Split(body, "\n")
	if len(bodyLines) <= maxLines {
		return body
	}
	kept := strings.Join(bodyLines[:maxLines], "\n")
	more := len(bodyLines) - maxLines
	rest := strings.Join(bodyLines[maxLines:], "\n")
	ra, rd := countDiffStats(rest)
	fold := fmt.Sprintf("… %d more lines", more)
	if ra > 0 || rd > 0 {
		fold = fmt.Sprintf("… %d more lines (+%d −%d)", more, ra, rd)
	}
	return kept + "\n" + fold
}
