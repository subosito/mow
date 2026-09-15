package tty

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rivo/uniseg"
	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"golang.org/x/term"
)

// One writer owns token/progress output so parallel tools cannot corrupt it.
type renderer struct {
	mu                          sync.Mutex
	out, errOut                 io.Writer
	interactive, color, verbose bool
	status, lineOpen            bool
	started                     time.Time
	tools                       int
	streamed                    bool
}

func newRenderer(out, errOut io.Writer, interactive, color, verbose bool) *renderer {
	return &renderer{out: out, errOut: errOut, interactive: interactive, color: color, verbose: verbose}
}

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func (r *renderer) clearStatus() {
	if r.status {
		fmt.Fprint(r.errOut, "\r\x1b[2K")
		r.status = false
	}
}

func (r *renderer) showStatus(text string) {
	if !r.interactive {
		return
	}
	r.clearStatus()
	if r.lineOpen {
		fmt.Fprintln(r.out)
		r.lineOpen = false
	}
	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil {
		width = 80
	}
	text = fitStatus(text, max(0, width-1))
	if r.color {
		fmt.Fprintf(r.errOut, "\x1b[2m%s\x1b[0m", text)
	} else {
		fmt.Fprint(r.errOut, text)
	}
	r.status = true
}

func fitStatus(text string, width int) string {
	g := uniseg.NewGraphemes(text)
	var out strings.Builder
	for g.Next() {
		width -= g.Width()
		if width < 0 {
			break
		}
		out.WriteString(g.Str())
	}
	return out.String()
}

func (r *renderer) begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streamed = false
	r.tools = 0
	r.started = time.Now()
	if r.interactive {
		if r.color {
			fmt.Fprint(r.errOut, "\n\x1b[1mmow\x1b[0m\n")
		} else {
			fmt.Fprint(r.errOut, "\nmow\n")
		}
	}
	r.showStatus("Working...")
}

func (r *renderer) token(delta string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearStatus()
	r.streamed = r.streamed || delta != ""
	text := safeText(delta)
	fmt.Fprint(r.out, text)
	if text != "" {
		r.lineOpen = !strings.HasSuffix(text, "\n")
	}
}

func (r *renderer) event(ev mow.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev.Type {
	case mow.EventToolStart:
		r.tools++
		// Tokens before a tool call belong to an intermediate model response.
		r.streamed = false
		if r.verbose {
			r.clearStatus()
			if r.lineOpen {
				fmt.Fprintln(r.out)
				r.lineOpen = false
			}
			fmt.Fprintf(r.errOut, "  %s\n", safeText(cliutil.FormatToolProgress(ev.Tool, ev.Args)))
		} else {
			r.showStatus("Running " + safeText(ev.Tool) + "...")
		}
	case mow.EventToolEnd:
		if ev.Denied || ev.Error != "" {
			r.clearStatus()
			message := ev.Error
			if message == "" {
				message = "denied"
			}
			fmt.Fprintf(r.errOut, "  %s: %s\n", safeText(ev.Tool), safeText(message))
		}
	}
}

func (r *renderer) finish(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearStatus()
	// Some providers do not emit deltas even when streaming is requested.
	if text != "" && !r.streamed {
		fmt.Fprint(r.out, safeText(text))
		r.lineOpen = !strings.HasSuffix(text, "\n")
	}
	if r.lineOpen {
		fmt.Fprintln(r.out)
		r.lineOpen = false
	}
	if r.interactive {
		fmt.Fprintf(r.errOut, "\n%.1fs", time.Since(r.started).Seconds())
		if r.tools > 0 {
			fmt.Fprintf(r.errOut, "  %d tool calls", r.tools)
		}
		fmt.Fprint(r.errOut, "\n\n")
	}
}
