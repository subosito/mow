package mowi

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/review"
)

// reviewDoneMsg is the result of an async /review or /sec run.
type reviewDoneMsg struct {
	cmd     string // "review" | "sec"
	text    string // framed report body for the transcript
	summary string // one-line status chip (counts)
	err     error
}

// handleReviewSlash runs /review or /sec (same workflow as mow review / mow sec).
// Optional args mirror the CLI: --staged, --diff, --base, --budget, paths…
func (m *model) handleReviewSlash(parts []string) tea.Cmd {
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.TrimPrefix(parts[0], "/")
	if cmd != "review" && cmd != "sec" {
		return nil
	}
	if m.busy {
		m.add(kindError, cmd+" · wait for the current turn to finish")
		m.refreshVP()
		return nil
	}
	rest := ""
	if len(parts) > 1 {
		rest = strings.Join(parts[1:], " ")
	}
	if isReviewHelp(rest) {
		m.add(kindStatus, reviewSlashHelp(cmd))
		m.refreshVP()
		return nil
	}

	prof := review.GeneralProfile()
	if cmd == "sec" {
		prof = review.SecurityProfile()
	}

	// Parse the same flag surface as the CLI (no engine flags — use live eng).
	fs := flag.NewFlagSet("mowi /"+cmd, flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{}) // silence default usage
	var rf review.CLIFlags
	rf.Bind(fs)
	args := strings.Fields(rest)
	if err := fs.Parse(args); err != nil {
		m.add(kindError, fmt.Sprintf("/%s: %v\n%s", cmd, err, reviewSlashHelp(cmd)))
		m.refreshVP()
		return nil
	}
	paths := fs.Args()
	ws := m.eng.Workspace()
	req, _, _, err := rf.Resolve(prof, ws, paths)
	if err != nil {
		m.add(kindError, "/"+cmd+": "+err.Error())
		m.refreshVP()
		return nil
	}

	m.showWelcome = false
	m.busy = true
	m.followBottom = true
	m.startedAt = time.Now()
	m.toolCurrent = cmd
	m.ta.SetHeight(inputMinHeight)
	m.syncInputChrome()
	m.add(kindStatus, fmt.Sprintf("%s · starting (model %s)…", cmd, short(m.eng.Model(), 40)))
	m.layout()
	m.refreshVP()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	eng := m.eng

	return tea.Batch(
		m.scheduleBusyHeartbeat(),
		m.pollToolUI(),
		func() tea.Msg {
			return runReviewMsg(ctx, eng, cmd, req)
		},
	)
}

func runReviewMsg(ctx context.Context, eng *mow.Engine, cmd string, req review.Request) tea.Msg {
	sc, err := review.ResolveScope(ctx, req.Scope)
	if err != nil {
		return reviewDoneMsg{cmd: cmd, err: err}
	}
	if sc.Empty() {
		return reviewDoneMsg{
			cmd:  cmd,
			text: fmt.Sprintf("%s · no files in scope — nothing was reviewed\n(check selector, --exclude, or --budget)", cmd),
		}
	}
	res, err := review.Run(ctx, review.NewEngineReviewer(eng), sc, req)
	if err != nil {
		if ctx.Err() != nil {
			return reviewDoneMsg{cmd: cmd, err: context.Canceled}
		}
		return reviewDoneMsg{cmd: cmd, err: err}
	}
	var buf bytes.Buffer
	if err := review.RenderText(&buf, res.Report, review.TextOptions{
		Color:   false, // TUI theme paints chrome; raw ANSI fights lipgloss
		Verbose: false,
	}); err != nil {
		return reviewDoneMsg{cmd: cmd, err: err}
	}
	body := strings.TrimRight(buf.String(), "\n")
	return reviewDoneMsg{
		cmd:     cmd,
		text:    frameReviewReport(cmd, res.Report, body),
		summary: reviewStatusSummary(cmd, res.Report),
	}
}

// isReviewHelp is only explicit help tokens — bare /review runs default scope.
func isReviewHelp(rest string) bool {
	switch strings.TrimSpace(rest) {
	case "help", "-h", "--help", "?":
		return true
	default:
		return false
	}
}

func reviewSlashHelp(cmd string) string {
	return strings.TrimSpace(fmt.Sprintf(`/%s [paths…] [flags] — same workflow as mow %s

  scope:   --diff main...HEAD | --staged | --base origin/main | paths…
  budget:  --budget small|medium|large
  filter:  --min-severity high | --include-low | --include-unverified
  other:   --no-verify (skip verification pass)

  default scope: uncommitted changes, or the whole tree if clean
  cancel: esc / ctrl+c while running`, cmd, cmd))
}

// applyReviewDone ends the busy review UI and paints the report.
func (m *model) applyReviewDone(msg reviewDoneMsg) tea.Cmd {
	m.busy = false
	m.cancel = nil
	m.toolCurrent = ""
	m.toolCurrentArgs = ""
	m.syncInputChrome()
	if msg.err != nil {
		errStr := strings.ToLower(msg.err.Error())
		if errors.Is(msg.err, context.Canceled) || strings.Contains(errStr, "context canceled") {
			m.add(kindStatus, msg.cmd+" · cancelled")
		} else {
			m.add(kindError, msg.cmd+": "+msg.err.Error())
		}
		m.layout()
		m.refreshVP()
		return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
	}
	text := strings.TrimSpace(msg.text)
	if text == "" {
		m.add(kindStatus, msg.cmd+" · done (empty report)")
	} else {
		// Status chip carries counts; body is a framed section (rules, not boxes).
		chip := strings.TrimSpace(msg.summary)
		if chip == "" {
			chip = msg.cmd + " · report"
		}
		m.add(kindStatus, chip)
		m.add(kindAssistant, text)
	}
	m.layout()
	m.refreshVP()
	return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
}

// frameReviewReport builds a borderless transcript section from a structured
// mow review.Report plus RenderText body — no prose parsing (T4).
func frameReviewReport(cmd string, rep *review.Report, body string) string {
	var b strings.Builder
	title := cmd + " · report"
	if rep != nil && strings.TrimSpace(rep.Profile) != "" {
		title += " · " + rep.Profile
	}
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if rep != nil {
		if sel := strings.TrimSpace(rep.Scope.Selection); sel != "" {
			fmt.Fprintf(&b, "scope · %s\n", sel)
		} else if rep.Scope.FilesReviewed > 0 {
			fmt.Fprintf(&b, "scope · %d files reviewed\n", rep.Scope.FilesReviewed)
		}
		if line := reviewCountsMeta(rep); line != "" {
			fmt.Fprintf(&b, "counts · %s\n", line)
		}
		if s := strings.TrimSpace(rep.Summary); s != "" {
			fmt.Fprintf(&b, "summary · %s\n", s)
		}
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	return b.String()
}

func reviewStatusSummary(cmd string, rep *review.Report) string {
	if rep == nil {
		return cmd + " · report"
	}
	n := len(rep.Findings)
	base := fmt.Sprintf("%s · report · %d finding", cmd, n)
	if n != 1 {
		base += "s"
	}
	if extra := reviewCountsMeta(rep); extra != "" && n > 0 {
		base += " · " + extra
	}
	return base
}

func reviewCountsMeta(rep *review.Report) string {
	if rep == nil {
		return ""
	}
	c := rep.Counts
	var parts []string
	add := func(label string, n int) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", label, n))
		}
	}
	add("critical", c.Critical)
	add("high", c.High)
	add("medium", c.Medium)
	add("low", c.Low)
	add("info", c.Info)
	if len(parts) > 0 {
		if rep.Suppressed > 0 {
			parts = append(parts, fmt.Sprintf("suppressed %d", rep.Suppressed))
		}
		return strings.Join(parts, " · ")
	}
	if n := len(rep.Findings); n > 0 {
		return fmt.Sprintf("%d total", n)
	}
	if rep.Suppressed > 0 {
		return fmt.Sprintf("suppressed %d", rep.Suppressed)
	}
	return "none"
}
