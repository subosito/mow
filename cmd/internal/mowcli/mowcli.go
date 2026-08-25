// Package mowcli is the shared mow command-line frontend. It implements the
// core commands (run, tty, trust, doctor, approvals, version, help) plus
// dispatch for whatever extensions/packs the embedding main package
// blank-imports — packs own their subcommands via ext.RegisterCommand, so a
// drop-in binary's feature set is exactly its import list.
//
// Two stock binaries share it: cmd/mow (lean: acp, rpc, focus, proc, cmdhook,
// mcp) and cmd/mow-full (those plus goal, job, ops, review, media).
package mowcli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/slash"
)

// Main runs the CLI over args (os.Args[1:]) and returns the exit status.
// The embedding binary's linked packs — its blank imports — define which
// subcommands exist on top of the core ones.
func Main(args []string) int {
	return run(args)
}

func run(args []string) int {
	if len(args) == 0 {
		if isTTY() {
			if c, ok := ext.DefaultInteractiveCommand(); ok {
				return c.Run(nil)
			}
		}
		printUsage()
		return 0
	}
	switch args[0] {
	case "run":
		return runCmd(args[1:])
	case "tty":
		return ttyCmd(args[1:])
	case "trust":
		return cliutil.TrustCommand("mow", args[1:])
	case "doctor":
		return doctorCmd(args[1:])
	case "approvals":
		return approvalsCmd(args[1:])
	case "version", "-v", "--version":
		fmt.Println(mow.VersionString())
		return 0
	case "help":
		return helpCmd(args[1:])
	case "-h", "--help":
		printUsage()
		return 0
	default:
		if c, ok := ext.LookupCommand(args[0]); ok {
			return c.Run(args[1:])
		}
		// Free-form args: treat as a prompt, but catch likely subcommand
		// typos and reserved/command-shaped leftovers first.
		if !strings.HasPrefix(args[0], "-") {
			if reservedCLIToken(args[0]) {
				fmt.Fprintf(os.Stderr, "mow: unknown command %q\n", args[0])
				if sug := suggestCommand(args[0]); sug != "" {
					fmt.Fprintf(os.Stderr, "  did you mean %q?\n", sug)
				}
				fmt.Fprintf(os.Stderr, "  for a free-form prompt use: mow run -p %q\n", args[0])
				return 2
			}
			if sug := suggestCommand(args[0]); sug != "" && len(args) == 1 {
				fmt.Fprintf(os.Stderr, "mow: unknown command %q (did you mean %q?)\n", args[0], sug)
				fmt.Fprintf(os.Stderr, "  for a free-form prompt use: mow run -p %q\n", args[0])
				return 2
			}
			prompt := strings.Join(args, " ")
			// Only nudge interactive users; keep scripted free-form runs quiet.
			if isTTY() {
				fmt.Fprintf(os.Stderr, "mow: treating as prompt (use `mow run -p …` or a known subcommand)\n")
			}
			return runCmd([]string{"-p", prompt})
		}
		fmt.Fprintf(os.Stderr, "mow: unknown command %q\n", args[0])
		printUsage()
		return 2
	}
}

// suggestCommand returns a close core/pack command name, or "".
// helpCmd routes `mow help <command>` to the same command-specific help
// users get from `mow <command> help`.
func helpCmd(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}
	switch args[0] {
	case "run":
		return runCmd(append([]string{"help"}, args[1:]...))
	case "tty":
		return ttyCmd(append([]string{"help"}, args[1:]...))
	case "trust":
		return cliutil.TrustCommand("mow", append([]string{"help"}, args[1:]...))
	default:
		if c, ok := ext.LookupCommand(args[0]); ok {
			return c.Run(append([]string{"help"}, args[1:]...))
		}
		fmt.Fprintf(os.Stderr, "mow help: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, "  run `mow help` to list available commands")
		return 2
	}
}

func suggestCommand(name string) string {
	cands := []string{"run", "tty", "trust", "doctor", "approvals", "version", "help"}
	for _, c := range ext.Commands() {
		cands = append(cands, c.Name)
	}
	return cliutil.SuggestCommand(name, cands)
}

// reservedCLIToken is a leftover that should never become a free-form prompt:
// known command family, or a close typo of one (rpc/ops/help/run/…).
func reservedCLIToken(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	switch n {
	case "run", "tty", "trust", "doctor", "approvals", "version", "help",
		"rpc", "ops", "repl", "acp", "goal", "review", "sec", "job", "proc",
		"mcp", "media", "focus":
		return true
	}
	if _, ok := ext.LookupCommand(n); ok {
		return true
	}
	return false
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func runCmd(args []string) int {
	// Help only when it is the first token, so `mow run -p "help …"` and
	// free-form prompts containing the word "help" still reach the model.
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printRunUsage()
		return 0
	}
	fs := cliutil.NewFlagSet("run")
	promptFlag := fs.String("p", "", "one-shot prompt")
	var ephemeral bool
	fs.BoolVar(&ephemeral, "ephemeral", false, "run against current context without saving this turn")
	fs.BoolVar(&ephemeral, "e", false, "shorthand for --ephemeral")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	prompt := strings.TrimSpace(*promptFlag)
	if prompt == "" {
		prompt = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "mow run: prompt required (-p or args)")
		fmt.Fprintln(os.Stderr, "  mow run -p \"…\" [flags]   or   mow run help")
		return 2
	}
	opt := ef.OptionsCLI()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	eng, err := mow.New(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow: %v\n", err)
		return 1
	}
	defer eng.Close()
	// Surface approximate input cost before the round trip (especially --continue).
	cliutil.PrintPromptCostEstimate(eng)
	res, err := eng.PromptWith(ctx, prompt, mow.PromptOpts{Ephemeral: ephemeral})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "mow: cancelled")
			return 130
		}
		fmt.Fprintf(os.Stderr, "mow: %v\n", err)
		if res.Text != "" {
			// Keep stdout clean on failure so pipelines don't misread partial
			// output as a successful run; send it to stderr with a marker.
			fmt.Fprintln(os.Stderr, "--- partial output before error ---")
			fmt.Fprintln(os.Stderr, res.Text)
		}
		return 1
	}
	fmt.Println(res.Text)
	if res.SessionID != "" && !ef.NoSession && !ephemeral {
		fmt.Fprintf(os.Stderr, "session=%s\n", res.SessionID)
	}
	return 0
}

// trustCmd manages the out-of-band workspace trust list ($MOW_HOME/trusted).
// Trust gates project-local .mow/config.yaml and skills; it is stored under
// the user home so a cloned repo can never grant itself trust.
func ttyCmd(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printTtyUsage()
		return 0
	}
	fs := cliutil.NewFlagSet("tty")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opt := ef.OptionsCLI()
	eng, err := mow.New(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow tty: %v\n", err)
		return 1
	}
	defer eng.Close() // tear down session-scoped resources (packs/proc procs) on exit
	fmt.Fprintln(os.Stderr, "mow tty — line session (not the full TUI); empty line or /quit to exit; Ctrl+C aborts the current turn")
	fmt.Fprintln(os.Stderr, "  /btw <question>  ask an aside without adding it to context")
	fmt.Fprintln(os.Stderr, "  /model [id]      list models or switch (catalog wire when present)")
	if ef.Stream {
		fmt.Fprintln(os.Stderr, "(token stream on stderr via --stream)")
	}
	// --continue / --session use the same Options path as mow run; surface that here.
	printTtySession(eng, ef)
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stderr, "mow> ")
		if !sc.Scan() {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "/quit" || line == "/exit" {
			break
		}
		// Slash meta-commands (not sent to the model).
		if strings.HasPrefix(line, "/") {
			if handled, err := handleTtySlash(context.Background(), eng, line); handled {
				if err != nil {
					fmt.Fprintf(os.Stderr, "mow: %v\n", err)
				}
				continue
			}
		}
		// /btw <text>: an aside answered against context but not persisted, so it
		// never re-enters a later prompt. Handy for a quick side question.
		btw := false
		if rest, ok := strings.CutPrefix(line, "/btw"); ok {
			line = strings.TrimSpace(rest)
			if line == "" {
				fmt.Fprintln(os.Stderr, "usage: /btw <question>  (aside — not added to context)")
				continue
			}
			btw = true
			fmt.Fprintln(os.Stderr, "(btw — this exchange won't be kept in context)")
		}
		if ef.Stream {
			fmt.Fprint(os.Stderr, "\n")
		}
		// Surface approximate input cost when context is already large.
		cliutil.PrintPromptCostEstimate(eng)
		// Per-prompt cancel: first Ctrl+C aborts this turn only; session stays up.
		pctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		res, err := eng.PromptWith(pctx, line, mow.PromptOpts{Ephemeral: btw})
		stop()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "mow: cancelled")
			} else {
				fmt.Fprintf(os.Stderr, "mow: %v\n", err)
			}
		}
		if res.Text != "" {
			if ef.Stream {
				fmt.Fprint(os.Stderr, "\n")
			}
			fmt.Println(res.Text)
		}
	}
	printTtySessionExit(eng, ef)
	return 0
}

// handleTtySlash runs meta slash commands. Returns handled=true when the line
// was a known command (and must not be sent as a user prompt).
func handleTtySlash(ctx context.Context, eng *mow.Engine, line string) (handled bool, err error) {
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
		return true, ttyModel(ctx, eng, filter)
	case "/help", "/?":
		fmt.Fprintln(os.Stderr, ttyHelp())
		return true, nil
	default:
		// Pack-registered commands. Anything not registered is not a command:
		// fall through so the line reaches the model as a prompt, because a
		// user who types "/tmp is full" means it as a sentence.
		c, ok := slash.Lookup(parts[0])
		if !ok {
			return false, nil
		}
		return true, ttyRunSlash(ctx, eng, c, parts)
	}
}

// ttyRunSlash executes a pack-registered slash command and prints it for a
// plain terminal: status line on stderr (so a piped stdout stays the report),
// body on stdout. The Rust mowi RPC host provides the other presentation — one
// behavior, two presentations.
func ttyRunSlash(ctx context.Context, eng *mow.Engine, c slash.Command, parts []string) error {
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
		// A plain terminal is exactly where ANSI belongs; the TUI is the one
		// that turns this off.
		Color: true,
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

// ttyHelp lists built-in commands plus whatever packs are linked, so the help
// text cannot drift from what actually dispatches.
func ttyHelp() string {
	var b strings.Builder
	b.WriteString("commands: /model [id]  /btw <q>  /help  /quit (or /exit)")
	for _, line := range slash.HelpLines() {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	return b.String()
}

// ttyModel lists GET /models or switches. Catalog wire is applied when present;
// empty wire keeps the current/default wire (SetModelWithWire).
func ttyModel(ctx context.Context, eng *mow.Engine, filter string) error {
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

// printTtySession announces session id and any resumed transcript (stderr).
// --continue works on tty the same as run (Options.Continue → load latest prior);
// without this banner it looks like a blank new chat.
func printTtySession(eng *mow.Engine, ef cliutil.EngineFlags) {
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
			fmt.Fprintf(os.Stderr, "  %s: %s\n", role, text)
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

// printTtySessionExit reminds how to resume this chat next time.
func printTtySessionExit(eng *mow.Engine, ef cliutil.EngineFlags) {
	if eng == nil || ef.NoSession {
		return
	}
	sid := eng.SessionID()
	if sid == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "session=%s\n", sid)
	fmt.Fprintf(os.Stderr, "resume: mow tty --session %s\n", sid)
	fmt.Fprintf(os.Stderr, "        mow tty --continue\n")
}

func printRunUsage() {
	fmt.Fprintf(os.Stderr, `mow run — one-shot prompt

  mow run -p "…" [flags]
  mow run "free-form prompt text" [flags]

Flags:

  -p TEXT              prompt (or pass as args)
  -e, --ephemeral      run against resumed context without saving this turn
  --config --workspace --model --base-url
  --allow-shell --allow-write --max-turns --effort --extra-root
  --sandbox             bubblewrap jail for bash/proc (Linux only; host fs
                        read-only, workspace rw, network on; --sandbox=none off)
  --stream --verbose --session --continue --no-session

Examples:

  mow run -p "summarize this repo"
  mow run -p "fix the tests" --allow-write --allow-shell
  mow run -p "run the suite" --allow-shell --sandbox
  mow run --continue -p "try again"
  mow run --continue -e -p "thanks"

`)
}

func printTtyUsage() {
	fmt.Fprintf(os.Stderr, `mow tty — interactive line session (plain terminal; not the full TUI)

  mow tty [flags]

In-session:

  /model [id]     list models or switch (catalog wire when present)
  /btw <q>        aside — answered but not kept in context
  /quit           exit (empty line also exits)
  Ctrl+C          cancel current turn only

Flags: same as mow run (--config --model --workspace --allow-write …).
  --stream        token deltas on stderr
  --continue      resume latest session
  --session ID    resume a specific session

Start an interactive session explicitly with mow tty.

`)
}

func printCmdGroup(title string, cmds []ext.Command) {
	if len(cmds) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, title)
	for _, c := range cmds {
		extra := ""
		if c.DefaultInteractive {
			extra = "  [default on TTY]"
		}
		fmt.Fprintf(os.Stderr, "  mow %-10s %s%s\n", c.Name, c.Summary, extra)
	}
	fmt.Fprintln(os.Stderr, "  (each: mow <name> help)")
	fmt.Fprintln(os.Stderr)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow — agent harness (library + CLI)

Core:

  mow run  -p "…" [flags]     one-shot prompt
  mow tty  [flags]            interactive line session (/model /btw /quit)
  mow trust [path]            trust workspace for project .mow config
  mow trust --list | --revoke
  mow doctor [--bundle]       inspect host/workspace (does not start MCP)
  mow approvals               list | remember allow|deny <tool> | revoke <id>
  mow version | help

`)
	if cmds := ext.Commands(); len(cmds) > 0 {
		var extensions, packs []ext.Command
		for _, c := range cmds {
			if strings.EqualFold(c.Layer, "pack") {
				packs = append(packs, c)
			} else {
				extensions = append(extensions, c)
			}
		}
		printCmdGroup("Extensions (this binary):", extensions)
		printCmdGroup("Packs (this binary):", packs)
	}
	fmt.Fprintf(os.Stderr, `Common flags:

  --config --workspace --model --effort --base-url --extra-root
  --allow-shell --allow-write --sandbox (Linux) --max-turns --stream --verbose
  --session --continue --no-session

Env:

  MOW_HOME                         data root (default ~/.mow)
  MOW_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY
  MOW_MODEL / OPENAI_MODEL / ANTHROPIC_MODEL
  MOW_EFFORT                       none | low | medium | high
  MOW_BASE_URL / OPENAI_BASE_URL / ANTHROPIC_BASE_URL
  MOW_WIRE                         openai-chat-completions | openai-responses | anthropic-messages
  MOW_OPS                          ops profile name (with packs/ops; mow-full)
  MOW_TRUST_PROJECT=1              trust project config this run

Defaults: tools read, glob, grep. Power: --allow-write / --allow-shell.
Library: import "github.com/subosito/mow" → Engine.Prompt

`)
}
