package mowi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/subosito/mow/slash"
)

// Generic dispatch for pack-registered slash commands. The TUI names no pack:
// `/review`, `/sec`, and anything a future pack registers appear here purely
// because the pack is linked, and disappear with its blank import. What the
// TUI owns is presentation — busy state, cancel wiring, a status chip and a
// framed transcript section — while the pack owns behavior.

// packSlashDoneMsg is the result of an async pack slash command.
type packSlashDoneMsg struct {
	cmd     string // invoked token, without the slash
	title   string
	body    string
	elapsed time.Duration
	err     error
}

// isPackSlash reports whether a token dispatches to a registered command.
func isPackSlash(token string) bool {
	_, ok := slash.Lookup(token)
	return ok
}

// handlePackSlash runs a registered command. Returns nil when the token is not
// registered, so the caller can fall through to built-in commands.
func (m *model) handlePackSlash(parts []string) (tea.Cmd, bool) {
	if len(parts) == 0 {
		return nil, false
	}
	c, ok := slash.Lookup(parts[0])
	if !ok {
		return nil, false
	}
	invoked := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]

	// Exclusive commands drive the same engine as the conversation; running
	// one mid-turn would interleave two conversations on one history.
	if c.Exclusive && m.busy {
		m.add(kindError, invoked+" · wait for the current turn to finish")
		m.refreshVP()
		return nil, true
	}

	// Help is synchronous: no engine work, so no busy state or spinner.
	if slash.IsHelpArgs(args) {
		usage := strings.TrimSpace(c.Usage)
		if usage == "" {
			usage = "/" + c.Name + " — " + c.Summary
		}
		m.add(kindStatus, usage)
		m.refreshVP()
		return nil, true
	}

	m.showWelcome = false
	m.busy = true
	m.followBottom = true
	m.startedAt = time.Now()
	m.toolCurrent = invoked
	m.ta.SetHeight(inputMinHeight)
	m.syncInputChrome()
	m.add(kindStatus, fmt.Sprintf("%s · starting (model %s)…", invoked, short(m.eng.Model(), 40)))
	m.layout()
	m.refreshVP()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	eng := m.eng
	ws := eng.Workspace()
	started := time.Now()

	return tea.Batch(
		m.scheduleBusyHeartbeat(),
		m.pollToolUI(),
		func() tea.Msg {
			res, err := c.Run(ctx, slash.Request{
				Name:      c.Name,
				Invoked:   invoked,
				Args:      args,
				Engine:    eng,
				Workspace: ws,
				// The TUI paints its own theme; raw ANSI from a renderer
				// fights lipgloss layout and corrupts column geometry.
				Color: false,
			})
			if err != nil && ctx.Err() != nil {
				err = context.Canceled
			}
			return packSlashDoneMsg{
				cmd:     invoked,
				title:   res.Title,
				body:    res.Body,
				elapsed: time.Since(started),
				err:     err,
			}
		},
	), true
}

// applyPackSlashDone ends the busy UI and paints the result.
func (m *model) applyPackSlashDone(msg packSlashDoneMsg) tea.Cmd {
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
			m.add(kindError, msg.err.Error())
		}
		m.layout()
		m.refreshVP()
		return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
	}

	title := strings.TrimSpace(msg.title)
	body := strings.TrimSpace(msg.body)
	if title == "" && body == "" {
		m.add(kindStatus, msg.cmd+" · done (no output)")
		m.layout()
		m.refreshVP()
		return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
	}
	if title == "" {
		title = msg.cmd + " · done"
	}
	m.add(kindStatus, title)
	if body != "" {
		m.add(kindAssistant, framePackSlash(title, body))
	}
	m.layout()
	m.refreshVP()
	return tea.Batch(m.pollPerm(), m.pollToolUI(), textarea.Blink)
}

// framePackSlash builds a borderless transcript section: heading rule, then
// the pack's own body verbatim. The TUI does not parse the body — a pack that
// changes its report layout must not be able to break the frame.
func framePackSlash(title, body string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n---\n\n")
	b.WriteString(strings.TrimSpace(body))
	return b.String()
}
