// Package tty is an optional line-mode REPL for mow host binaries.
package tty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/reeflective/readline"
	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/slash"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:               "tty",
		Summary:            "interactive conversation",
		Layer:              "ext",
		DefaultInteractive: true,
		Run:                runCmd,
	})
}

func runCmd(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printUsage()
		return 0
	}
	fs := cliutil.NewFlagSet("tty")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	interactive := interactiveTerminal()
	ef.Stream = interactive
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := ef.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "mow tty: %v\n", err)
		return 2
	}
	output := newRenderer(os.Stdout, os.Stderr, interactive, colorEnabled(), ef.Verbose)
	opt := ef.Options()
	cliutil.EnableVerbose(ef.Verbose)
	opt.OnEvent = output.event
	if ef.Stream {
		opt.OnToken = output.token
	}
	eng, err := mow.NewHarness(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow tty: %v\n", err)
		return 1
	}
	defer eng.Close() // tear down session-scoped resources (ext/proc procs) on exit
	if interactive {
		permissions := "read only"
		if eng.AllowWrite() {
			permissions = "write enabled"
		}
		if eng.AllowShell() {
			permissions += ", shell enabled"
		}
		fmt.Fprintf(os.Stderr, "\nmow tty  %s\n%s  [%s]\n\n", safeText(eng.Model()), safeText(eng.Workspace()), permissions)
	}
	// --continue / --session use the same Options path as mow run; surface that here.
	printSession(eng, ef)
	input := newInput(os.Stdin, interactive)
	defer input.close()
	ctx, terminate := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer terminate()
	// Input is cancellable too, including while the editor has the terminal raw.
	inputDone := make(chan struct{})
	defer close(inputDone)
	go func() {
		select {
		case <-ctx.Done():
			input.close()
		case <-inputDone:
		}
	}()
	for {
		line, readErr := input.read()
		if ctx.Err() != nil {
			return 143
		}
		if errors.Is(readErr, readline.ErrInterrupt) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "mow tty: input: %v\n", readErr)
			return 1
		}
		line = strings.TrimSpace(line)
		if line == "/quit" || line == "/exit" {
			break
		}
		if line == "" {
			continue
		}
		pctx, stop := signal.NotifyContext(ctx, syscall.SIGINT)
		// Slash meta-commands (not sent to the model).
		if strings.HasPrefix(line, "/") {
			if handled, err := handleSlash(pctx, eng, line); handled {
				stop()
				if err != nil {
					fmt.Fprintf(os.Stderr, "mow: %v\n", err)
				}
				continue
			}
		}
		// /btw <text>: an aside answered against context but not persisted, so it
		// never re-enters a later prompt. Handy for a quick side question.
		btw := false
		if fields := strings.Fields(line); fields[0] == "/btw" {
			line = strings.TrimSpace(strings.TrimPrefix(line, "/btw"))
			if line == "" {
				stop()
				fmt.Fprintln(os.Stderr, "usage: /btw <question>  (aside — not added to context)")
				continue
			}
			btw = true
			fmt.Fprintln(os.Stderr, "(btw — this exchange won't be kept in context)")
		}
		// Surface approximate input cost when context is already large.
		cliutil.PrintPromptCostEstimate(eng)
		// Per-prompt cancel: first Ctrl+C aborts this turn only; session stays up.
		output.begin()
		res, err := eng.PromptWith(pctx, line, mow.PromptOpts{Ephemeral: btw})
		stop()
		output.finish(res.Text)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "mow: cancelled")
			} else {
				fmt.Fprintf(os.Stderr, "mow: %v\n", err)
			}
		}
		if ctx.Err() != nil {
			return 143
		}
	}
	printSessionExit(eng, ef)
	return 0
}

// handleSlash runs meta slash commands. Returns handled=true when the line
// was a known command (and must not be sent as a user prompt).
func handleSlash(ctx context.Context, eng *mow.Engine, line string) (handled bool, err error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, nil
	}
	switch parts[0] {
	case "/model":
		filter := ""
		if len(parts) > 1 {
			filter = strings.Join(parts[1:], " ")
		}
		return true, modelCmd(ctx, eng, filter)
	case "/help", "/?":
		fmt.Fprintln(os.Stderr, helpText())
		return true, nil
	default:
		// Pack-registered commands. Anything not registered is not a command:
		// fall through so the line reaches the model as a prompt, because a
		// user who types "/tmp is full" means it as a sentence.
		c, ok := slash.Lookup(parts[0])
		if !ok {
			return false, nil
		}
		return true, runSlash(ctx, eng, c, parts)
	}
}

// runSlash executes a pack-registered slash command and prints it for a
// plain terminal: status line on stderr (so a piped stdout stays the report),
// body on stdout. Host UIs over mow acp provide the other presentation — one
// behavior, two presentations.
func runSlash(ctx context.Context, eng *mow.Engine, c slash.Command, parts []string) error {
	ws := ""
	if eng != nil {
		ws = eng.Workspace()
	}
	req := slash.Request{
		Name:      c.Name,
		Invoked:   strings.TrimPrefix(parts[0], "/"),
		Args:      parts[1:],
		Engine:    eng,
		Workspace: ws,
		// Respect redirection and NO_COLOR for extension-owned rendering too.
		Color: colorEnabled(),
	}
	res, err := c.Run(ctx, req)
	if err != nil {
		return err
	}
	if t := strings.TrimSpace(res.Title); t != "" {
		fmt.Fprintln(os.Stderr, t)
	}
	if b := strings.TrimRight(res.Body, "\n"); b != "" {
		fmt.Println(b)
	}
	return nil
}

// helpText lists built-in commands plus whatever packs are linked, so the help
// text cannot drift from what actually dispatches.
func helpText() string {
	var b strings.Builder
	b.WriteString("commands: /model [id]  /btw <q>  /help  /quit (or /exit)")
	for _, line := range slash.HelpLines() {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	return b.String()
}

// modelCmd lists GET /models or switches. Catalog wire is applied when present;
// empty wire keeps the current/default wire (SetModelWithWire).
func modelCmd(ctx context.Context, eng *mow.Engine, filter string) error {
	if eng == nil {
		return fmt.Errorf("nil engine")
	}
	filter = strings.TrimSpace(filter)
	// Allow paste of "id  [wire]" display lines.
	if i := strings.LastIndex(filter, "["); i > 0 && strings.HasSuffix(filter, "]") {
		filter = strings.TrimSpace(filter[:i])
	}
	listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, err := eng.ListModels(listCtx)
	cur := eng.Model()
	wire := eng.Wire()
	if err != nil {
		if filter == "" {
			return err
		}
		// Catalog down — still allow force-set by id.
		if setErr := eng.SetModel(filter); setErr != nil {
			return fmt.Errorf("list models: %w; set: %v", err, setErr)
		}
		fmt.Fprintf(os.Stderr, "model → %s · %s (catalog unavailable: %v)\n", eng.Model(), eng.Wire(), err)
		return nil
	}
	// Chat UI: hide image/search/speech facets and non-chat wires.
	list = mow.FilterChatModels(list)
	if filter == "" {
		fmt.Fprintf(os.Stderr, "models · current %s · wire %s\n", cur, wire)
		const maxShow = 80
		n := len(list)
		show := n
		if show > maxShow {
			show = maxShow
		}
		for i := 0; i < show; i++ {
			info := list[i]
			mark := "  "
			if cur != "" && strings.EqualFold(info.ID, cur) {
				mark = "• "
			}
			line := mark + info.ID
			if info.Wire != "" {
				line += "  [" + info.Wire + "]"
			}
			fmt.Fprintln(os.Stderr, line)
		}
		if n > show {
			fmt.Fprintf(os.Stderr, "… %d more — refine with /model <filter>\n", n-show)
		}
		if n == 0 {
			fmt.Fprintln(os.Stderr, "(empty catalog)")
		}
		fmt.Fprintln(os.Stderr, "switch: /model <id>")
		return nil
	}
	// Exact match → set + catalog wire when known.
	for _, info := range list {
		if strings.EqualFold(info.ID, filter) {
			if err := eng.SetModelWithWire(info.ID, info.Wire); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "model → %s · %s\n", eng.Model(), eng.Wire())
			return nil
		}
	}
	// Unique substring match.
	var matched []mow.ModelInfo
	fl := strings.ToLower(filter)
	for _, info := range list {
		if strings.Contains(strings.ToLower(info.ID), fl) {
			matched = append(matched, info)
		}
	}
	if len(matched) == 1 {
		if err := eng.SetModelWithWire(matched[0].ID, matched[0].Wire); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "model → %s · %s\n", eng.Model(), eng.Wire())
		return nil
	}
	if len(matched) == 0 {
		if len(list) > 0 {
			return fmt.Errorf("no catalog model matching %q — /model to list", filter)
		}
		if err := eng.SetModel(filter); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "model → %s · %s\n", eng.Model(), eng.Wire())
		return nil
	}
	fmt.Fprintf(os.Stderr, "models matching %q (%d) — pick an exact id:\n", filter, len(matched))
	for _, info := range matched {
		line := "  " + info.ID
		if info.Wire != "" {
			line += "  [" + info.Wire + "]"
		}
		fmt.Fprintln(os.Stderr, line)
	}
	return nil
}

// printSession announces session id and any resumed transcript (stderr).
// --continue works on tty the same as run (Options.Continue → load latest prior);
// without this banner it looks like a blank new chat.
func printSession(eng *mow.Engine, ef cliutil.EngineFlags) {
	if eng == nil || ef.NoSession {
		return
	}
	sid := eng.SessionID()
	if sid == "" {
		return
	}
	wantResume := ef.Continue || strings.TrimSpace(ef.SessionID) != ""
	tr := eng.Transcript()
	if wantResume && len(tr) > 0 {
		fmt.Fprintf(os.Stderr, "session=%s resumed (%d message(s))\n", sid, len(tr))
		if !interactiveTerminal() {
			return
		}
		if len(tr) > 6 {
			tr = tr[len(tr)-6:]
		}
		for _, m := range tr {
			role := m.Role
			if role == "" {
				role = "?"
			}
			text := strings.Join(strings.Fields(m.Content), " ")
			const max = 160
			if utf8.RuneCountInString(text) > max {
				r := []rune(text)
				text = string(r[:max-1]) + "…"
			}
			fmt.Fprintf(os.Stderr, "  %s: %s\n", safeText(role), safeText(text))
		}
		return
	}
	if wantResume {
		// Continue/session set but no UI transcript (empty or missing file).
		fmt.Fprintf(os.Stderr, "session=%s (no prior turns to show)\n", sid)
		return
	}
	fmt.Fprintf(os.Stderr, "session=%s\n", sid)
}

// printSessionExit reminds how to resume this chat next time.
func printSessionExit(eng *mow.Engine, ef cliutil.EngineFlags) {
	if eng == nil || ef.NoSession {
		return
	}
	sid := eng.SessionID()
	if sid == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "session=%s\n", sid)
	fmt.Fprintf(os.Stderr, "resume: mow tty --session %s\n", sid)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow tty — interactive line session

  mow tty [flags]

  /model [id]     list or switch models
  /btw <q>        aside (not kept in context)
  /quit           exit
  Ctrl+C          cancel the turn or clear input
  Ctrl+D          exit at an empty prompt
  Ctrl+J          insert a newline
  Tab             complete slash commands
  Up / Ctrl+R     recall or search this session's input history

Flags: same as mow run.
  --stream=false  disable streaming (on by default in a terminal)
  --continue --session ID

`)
}
