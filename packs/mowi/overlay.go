package mowi

import (
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// placeOverlay draws fg on top of bg at (x, y) in cell coordinates.
// Result is height lines, each width cells (ANSI-aware pad/truncate).
func placeOverlay(x, y int, fg, bg string, width, height int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	bgLines := padFrame(strings.Split(strings.TrimRight(bg, "\n"), "\n"), width, height)
	fgLines := strings.Split(strings.TrimRight(fg, "\n"), "\n")
	for i, line := range fgLines {
		row := y + i
		if row < 0 || row >= height {
			continue
		}
		bgLines[row] = spliceLine(bgLines[row], line, x, width)
	}
	return strings.Join(bgLines, "\n")
}

// placeOverlayCenter centers fg over bg within width×height.
func placeOverlayCenter(fg, bg string, width, height int) string {
	fgLines := strings.Split(strings.TrimRight(fg, "\n"), "\n")
	ow := 0
	for _, l := range fgLines {
		if w := xansi.StringWidth(l); w > ow {
			ow = w
		}
	}
	oh := len(fgLines)
	x := max(0, (width-ow)/2)
	y := max(0, (height-oh)/2)
	return placeOverlay(x, y, strings.TrimRight(fg, "\n"), bg, width, height)
}

func padFrame(lines []string, width, height int) []string {
	out := make([]string, height)
	for i := 0; i < height; i++ {
		var s string
		if i < len(lines) {
			s = lines[i]
		}
		out[i] = padLine(s, width)
	}
	return out
}

func padLine(s string, width int) string {
	s = xansi.Truncate(s, width, "")
	for xansi.StringWidth(s) < width {
		s += " "
	}
	return s
}

// spliceLine replaces cells [x, x+fgWidth) of bg with fg.
func spliceLine(bg, fg string, x, width int) string {
	if x < 0 {
		x = 0
	}
	if x >= width {
		return padLine(bg, width)
	}
	bg = padLine(bg, width)
	fg = xansi.Truncate(fg, max(1, width-x), "")
	fw := xansi.StringWidth(fg)
	left := xansi.Cut(bg, 0, x)
	for xansi.StringWidth(left) < x {
		left += " "
	}
	rightStart := x + fw
	right := ""
	if rightStart < width {
		right = xansi.Cut(bg, rightStart, width)
	}
	return padLine(left+fg+right, width)
}
