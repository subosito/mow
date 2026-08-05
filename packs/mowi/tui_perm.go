package mowi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/subosito/mow"
)

// perm / setPerm are the only access paths for permMode (atomic — see field).
func (m *model) perm() PermissionMode {
	return PermissionMode(m.permMode.Load())
}

func (m *model) setPerm(p PermissionMode) {
	m.permMode.Store(int32(p))
}

// isPowerTool delegates to mow so the ask-gate vocabulary can never drift
// from the harness's own power-tool list.
func isPowerTool(name string) bool {
	return mow.IsPowerTool(name)
}

// permPreview formats tool args for the approval box: the actual command for
// bash, path + content head for write, old/new for edit — approving raw JSON
// blind was the old behavior.
func permPreview(name string, raw []byte) string {
	switch strings.ToLower(name) {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &a) == nil && strings.TrimSpace(a.Command) != "" {
			return "$ " + truncate(a.Command, 400)
		}
	case "write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(raw, &a) == nil && a.Path != "" {
			return a.Path + "\n@@ write @@\n" + strings.TrimRight(diffPreviewLines("+ ", a.Content, 14), "\n")
		}
	case "edit":
		var a struct {
			Path string `json:"path"`
			Old  string `json:"old_string"`
			New  string `json:"new_string"`
		}
		if json.Unmarshal(raw, &a) == nil && a.Path != "" {
			return a.Path + "\n@@ replace @@\n" +
				diffPreviewLines("- ", a.Old, 10) + strings.TrimRight(diffPreviewLines("+ ", a.New, 10), "\n")
		}
	}
	return truncate(string(raw), 180)
}

// diffPreviewLines prefixes each line (± for a diff) and caps the count, so the
// approval box shows a real before/after instead of a JSON blob.
func diffPreviewLines(prefix, s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	n := len(lines)
	if n > maxLines {
		lines = lines[:maxLines]
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(prefix + truncate(l, 120) + "\n")
	}
	if n > maxLines {
		b.WriteString(fmt.Sprintf("… (+%d more)\n", n-maxLines))
	}
	return b.String()
}

func (m *model) handlePermKey(k string) tea.Cmd {
	req := m.permWait
	if req == nil {
		return nil
	}
	// Cancel always works immediately (escape hatch).
	switch k {
	case "ctrl+c", "esc":
		m.clearPermWait()
		req.resp <- fmt.Errorf("cancelled")
		if m.cancel != nil {
			m.cancel()
		}
		m.add(kindStatus, "cancelled")
		m.layout()
		m.refreshVP()
		return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
	}
	// y/n/a require the strip to have painted and a short arm window so a
	// keystroke already in flight cannot approve an unread shell command.
	if k == "y" || k == "Y" || k == "n" || k == "N" || k == "a" || k == "A" {
		if !m.permDecisionArmed() {
			return nil
		}
	}
	switch k {
	case "y", "Y":
		m.clearPermWait()
		req.resp <- nil
		m.add(kindTool, "allowed · "+req.name)
	case "n", "N":
		m.clearPermWait()
		req.resp <- fmt.Errorf("denied by user")
		m.add(kindError, "denied · "+req.name)
	case "a", "A":
		m.clearPermWait()
		m.autoPower.Store(true)
		m.setPerm(PermAuto)
		req.resp <- nil
		m.add(kindStatus, "power tools always allowed this session")
	default:
		return nil
	}
	m.layout()
	m.refreshVP()
	return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
}

// permArmWindow is how long after the strip first paints before y/n/a count.
const permArmWindow = 280 * time.Millisecond

func (m *model) permDecisionArmed() bool {
	if m.permWait == nil || !m.permStripShown || m.permArmedAt.IsZero() {
		return false
	}
	return time.Since(m.permArmedAt) >= permArmWindow
}

func (m *model) clearPermWait() {
	m.permWait = nil
	m.permArmedAt = time.Time{}
	m.permStripShown = false
}

func (m *model) armPermWait(msg *permAskMsg) {
	m.permWait = msg
	m.permArmedAt = time.Now()
	m.permStripShown = false
}

// testArmPerm arms a permission as if the strip already painted (tests).
func (m *model) testArmPerm(name, args string, resp chan error) {
	m.armPermWait(&permAskMsg{name: name, args: args, resp: resp})
	m.permStripShown = true
	m.permArmedAt = time.Now().Add(-time.Second)
}

func (m *model) togglePerm() {
	if m.perm() == PermAuto {
		m.setPerm(PermAsk)
		m.autoPower.Store(false)
	} else {
		m.setPerm(PermAuto)
	}
	m.add(kindStatus, "perm "+glyphArrow+" "+m.perm().String())
	m.refreshVP()
}

// renderPermissionStrip is only for interactive tool permission prompts.
func (m *model) renderPermissionStrip() string {
	if m.permWait == nil {
		return ""
	}
	// First paint arms y/n/a decisions (see permDecisionArmed).
	m.permStripShown = true
	th := m.theme
	label := th.Warn.Render(glyphWarn + " permission")
	pending := len(m.permCh) + 1
	if pending > 1 {
		label += th.Muted.Render(fmt.Sprintf(" (%d of %d)", 1, pending))
	}
	if m.permWait.name != "" {
		label += th.Muted.Render("  " + m.permWait.name)
	}

	// Keep the decision keys pinned to the right edge. The command preview is
	// deliberately the part that yields, so y/n/a never wrap below it.
	ww := max(1, m.width)
	keyGap := "   "
	if ww < 56 {
		keyGap = " "
	}
	keys := th.Accent.Render("y") + th.Muted.Render(" allow"+keyGap) +
		th.Accent.Render("n") + th.Muted.Render(" deny"+keyGap) +
		th.Accent.Render("a") + th.Muted.Render(" always")
	keyW := lipgloss.Width(keys)
	// Status contributes one cell of padding at each edge.
	contentW := max(1, ww-2)
	left := label
	if m.permWait.args != "" {
		args := strings.ReplaceAll(m.permWait.args, "\n", " ⏎ ")
		budget := contentW - lipgloss.Width(label) - keyW - 3
		if budget > 0 {
			left += th.Muted.Faint(true).Render("  " + xansi.Truncate(args, budget, "…"))
		}
	}
	available := contentW - keyW - 1
	if lipgloss.Width(left) > available {
		left = xansi.Truncate(left, max(1, available), "…")
	}
	gap := max(1, contentW-lipgloss.Width(left)-keyW)
	line := left + strings.Repeat(" ", gap) + keys
	return th.Status.Render(xansi.Truncate(line, contentW, ""))
}
