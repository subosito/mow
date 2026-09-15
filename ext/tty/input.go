package tty

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/reeflective/readline"
	"github.com/subosito/mow/slash"
	"golang.org/x/term"
)

func interactiveTerminal() bool {
	return os.Getenv("TERM") != "dumb" && term.IsTerminal(int(os.Stdin.Fd())) &&
		term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func colorEnabled() bool {
	_, disabled := os.LookupEnv("NO_COLOR")
	return !disabled && interactiveTerminal()
}

type inputReader struct {
	shell   *readline.Shell
	scanner *bufio.Scanner
	source  io.ReadCloser
	closed  atomic.Bool
	once    sync.Once
}

func newInput(source io.ReadCloser, interactive bool) *inputReader {
	in := &inputReader{source: source}
	if !interactive {
		in.scanner = bufio.NewScanner(source)
		in.scanner.Buffer(make([]byte, 64*1024), 8<<20)
		return in
	}
	rl := readline.NewShell()
	rl.Prompt.Primary(func() string { return "you> " })
	rl.Prompt.Secondary(func() string { return "...  " })
	_ = rl.Config.Set("enable-bracketed-paste", true)
	_ = rl.Config.Set("history-autosuggest", false)
	_ = rl.Config.Set("colored-completion-prefix", false)
	_ = rl.Config.Set("colored-stats", false)
	_ = rl.Config.Set("completion-selection-style", "\x1b[7m")
	rl.Keymap.Register(map[string]func(){
		"tty-newline": func() { rl.Cursor().InsertAt('\n') },
		"tty-cancel": func() {
			rl.Display.AcceptLine()
			rl.History.Accept(false, false, readline.ErrInterrupt)
		},
	})
	for keymap := range rl.Config.Binds {
		_ = rl.Config.Bind(keymap, "\x03", "tty-cancel", false)
	}
	for _, keymap := range []string{"emacs", "vi-insert"} {
		_ = rl.Config.Bind(keymap, "\n", "tty-newline", false)
		_ = rl.Config.Bind(keymap, "\r", "accept-line", false)
		_ = rl.Config.Bind(keymap, "\x03", "tty-cancel", false)
	}
	rl.Completer = func(line []rune, cursor int) readline.Completions {
		if cursor < 0 || cursor > len(line) {
			return readline.CompleteValues()
		}
		prefix := string(line[:cursor])
		if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, " \t\n") {
			return readline.CompleteValues()
		}
		return readline.CompleteValues(commandNames()...)
	}
	in.shell = rl
	return in
}

func commandNames() []string {
	names := []string{"/model", "/btw", "/help", "/quit", "/exit"}
	for _, name := range slash.Names() {
		names = append(names, "/"+name)
	}
	return names
}

func (in *inputReader) read() (string, error) {
	if in.closed.Load() {
		return "", io.EOF
	}
	if in.shell != nil {
		return in.shell.Readline()
	}
	if in.scanner.Scan() {
		return in.scanner.Text(), nil
	}
	if err := in.scanner.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

func (in *inputReader) close() {
	in.once.Do(func() {
		in.closed.Store(true)
		if in.shell != nil {
			// Feed + wake are thread-safe. Readline restores terminal state
			// before returning; do not close its terminal descriptor here.
			in.shell.Keys.Feed(true, '\x03')
			in.shell.Keys.RequestRefresh()
		} else {
			_ = in.source.Close()
		}
	})
}
