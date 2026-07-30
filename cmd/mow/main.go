// Command mow is a thin shell: core commands (run, repl) plus whatever
// extension packs are blank-imported below. Packs own their subcommands via
// ext.RegisterCommand — drop an import and the subcommand disappears.
package main

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

	// Linked packs — each registers tools/commands in init.
	// Remove an import to drop that pack (and its subcommand) from this binary.
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/cmdhook"
	_ "github.com/subosito/mow/ext/goal"
	_ "github.com/subosito/mow/ext/job"
	_ "github.com/subosito/mow/ext/lsp"
	_ "github.com/subosito/mow/ext/mcp"
	_ "github.com/subosito/mow/ext/ops"
	_ "github.com/subosito/mow/ext/proc"
	_ "github.com/subosito/mow/ext/review"
	_ "github.com/subosito/mow/ext/rpc"
)

func main() {
	os.Exit(run(os.Args[1:]))
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
	case "repl":
		return replCmd(args[1:])
	case "trust":
		return trustCmd(args[1:])
	case "version", "-v", "--version":
		fmt.Println(mow.VersionString())
		return 0
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		if c, ok := ext.LookupCommand(args[0]); ok {
			return c.Run(args[1:])
		}
		// Free-form args: treat as a prompt, but catch likely subcommand typos first.
		if !strings.HasPrefix(args[0], "-") {
			if sug := suggestCommand(args[0]); sug != "" && len(args) == 1 {
				fmt.Fprintf(os.Stderr, "mow: unknown command %q (did you mean %q?)\n", args[0], sug)
				fmt.Fprintf(os.Stderr, "  for a free-form prompt use: mow run -p %q\n", args[0])
				return 2
			}
			prompt := strings.Join(args, " ")
			fmt.Fprintf(os.Stderr, "mow: treating as prompt (use `mow run -p …` or a known subcommand)\n")
			return runCmd([]string{"-p", prompt})
		}
		fmt.Fprintf(os.Stderr, "mow: unknown command %q\n", args[0])
		printUsage()
		return 2
	}
}

// suggestCommand returns a close core/pack command name, or "".
func suggestCommand(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	cands := []string{"run", "repl", "trust", "version", "help"}
	for _, c := range ext.Commands() {
		cands = append(cands, c.Name)
	}
	best, bestD := "", 3
	for _, c := range cands {
		d := editDistance(name, c)
		if d > 0 && d < bestD {
			bestD, best = d, c
		}
	}
	if bestD <= 2 {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	// Bounded DP for short command names.
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			ins, del, sub := cur[j-1]+1, prev[j]+1, prev[j-1]+cost
			cur[j] = ins
			if del < cur[j] {
				cur[j] = del
			}
			if sub < cur[j] {
				cur[j] = sub
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func runCmd(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printRunUsage()
			return 0
		}
	}
	fs := cliutil.NewFlagSet("run")
	promptFlag := fs.String("p", "", "one-shot prompt")
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
	res, err := mow.Run(ctx, prompt, opt)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "mow: cancelled")
			return 130
		}
		fmt.Fprintf(os.Stderr, "mow: %v\n", err)
		if res.Text != "" {
			fmt.Println(res.Text)
		}
		return 1
	}
	fmt.Println(res.Text)
	if res.SessionID != "" && !ef.NoSession {
		fmt.Fprintf(os.Stderr, "session=%s\n", res.SessionID)
	}
	return 0
}

// trustCmd manages the out-of-band workspace trust list ($MOW_HOME/trusted).
// Trust gates project-local .mow/config.yaml and skills; it is stored under
// the user home so a cloned repo can never grant itself trust.
func trustCmd(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Fprintf(os.Stderr, `mow trust — allow project .mow/config and skills

Trust is stored under $MOW_HOME/trusted (not inside the repo).

  mow trust [path]           trust this workspace (default: .)
  mow trust --list           list trusted workspaces
  mow trust --revoke [path]  revoke trust

  --workspace path           same as [path] (flag form)

`)
			return 0
		}
	}
	fs := cliutil.NewFlagSet("trust")
	list := fs.Bool("list", false, "show trusted workspaces")
	revoke := fs.Bool("revoke", false, "revoke trust instead of granting it")
	dir := fs.String("workspace", ".", "workspace to trust/revoke")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Positional path: mow trust /path/to/repo
	if fs.NArg() > 0 && (*dir == "." || *dir == "") {
		*dir = fs.Arg(0)
	}
	if *list {
		for _, ws := range mow.TrustedWorkspaces() {
			fmt.Println(ws)
		}
		return 0
	}
	if *revoke {
		if err := mow.RevokeWorkspaceTrust(*dir); err != nil {
			fmt.Fprintf(os.Stderr, "mow trust: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "mow: untrusted %s\n", *dir)
		return 0
	}
	if err := mow.TrustWorkspace(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "mow trust: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "mow: trusted %s  (project .mow/config.yaml + skills load)\n", *dir)
	return 0
}

func replCmd(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printReplUsage()
			return 0
		}
	}
	fs := cliutil.NewFlagSet("repl")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opt := ef.OptionsCLI()
	eng, err := mow.New(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow repl: %v\n", err)
		return 1
	}
	defer eng.Close() // tear down session-scoped resources (ext/proc procs) on exit
	fmt.Fprintln(os.Stderr, "mow repl — empty line or /quit to exit; Ctrl+C aborts the current turn")
	fmt.Fprintln(os.Stderr, "  /btw <question>  ask an aside without adding it to context")
	fmt.Fprintln(os.Stderr, "  /model [id]      list models or switch (catalog wire when present)")
	if ef.Stream {
		fmt.Fprintln(os.Stderr, "(token stream on stderr via --stream)")
	}
	// --continue / --session use the same Options path as mow run; surface that here.
	printReplSession(eng, ef)
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
			if handled, err := handleReplSlash(context.Background(), eng, line); handled {
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
		// Per-prompt cancel: first Ctrl+C aborts this turn only; REPL stays up.
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
	printReplSessionExit(eng, ef)
	return 0
}

// handleReplSlash runs meta slash commands. Returns handled=true when the line
// was a known command (and must not be sent as a user prompt).
func handleReplSlash(ctx context.Context, eng *mow.Engine, line string) (handled bool, err error) {
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
		return true, replModel(ctx, eng, filter)
	case "/help", "/?":
		fmt.Fprintln(os.Stderr, "commands: /model [id]  /btw <q>  /quit")
		return true, nil
	default:
		return false, nil
	}
}

// replModel lists GET /models or switches. Catalog wire is applied when present;
// empty wire keeps the current/default wire (SetModelWithWire).
func replModel(ctx context.Context, eng *mow.Engine, filter string) error {
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

// printReplSession announces session id and any resumed transcript (stderr).
// --continue works on repl the same as run (Options.Continue → load latest prior);
// without this banner it looks like a blank new chat.
func printReplSession(eng *mow.Engine, ef cliutil.EngineFlags) {
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

// printReplSessionExit reminds how to resume this chat next time.
func printReplSessionExit(eng *mow.Engine, ef cliutil.EngineFlags) {
	if eng == nil || ef.NoSession {
		return
	}
	sid := eng.SessionID()
	if sid == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "session=%s\n", sid)
	fmt.Fprintf(os.Stderr, "resume: mow repl --session %s\n", sid)
	fmt.Fprintf(os.Stderr, "        mow repl --continue\n")
}

func printRunUsage() {
	fmt.Fprintf(os.Stderr, `mow run — one-shot prompt

  mow run -p "…" [flags]
  mow run "free-form prompt text" [flags]

Flags:

  -p TEXT              prompt (or pass as args)
  --config --workspace --model --base-url
  --allow-shell --allow-write --max-turns
  --stream --verbose --session --continue --no-session

Examples:

  mow run -p "summarize this repo"
  mow run -p "fix the tests" --allow-write --allow-shell
  mow run --continue -p "try again"

`)
}

func printReplUsage() {
	fmt.Fprintf(os.Stderr, `mow repl — interactive line REPL

  mow repl [flags]

In-session:

  /model [id]     list models or switch (catalog wire when present)
  /btw <q>        aside — answered but not kept in context
  /quit           exit (empty line also exits)
  Ctrl+C          cancel current turn only

Flags: same as mow run (--config --model --workspace --allow-write …).
  --stream        token deltas on stderr
  --continue      resume latest session
  --session ID    resume a specific session

TTY with no args often lands here (default interactive pack if linked).

`)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow — agent harness (library + CLI)

Core:

  mow run  -p "…" [flags]     one-shot prompt
  mow repl [flags]            interactive REPL  (/model /btw /quit)
  mow trust [path]            trust workspace for project .mow config
  mow trust --list | --revoke
  mow version | help

`)
	if cmds := ext.Commands(); len(cmds) > 0 {
		fmt.Fprintln(os.Stderr, "Packs (this binary):")
		for _, c := range cmds {
			extra := ""
			if c.DefaultInteractive {
				extra = "  [default on TTY]"
			}
			fmt.Fprintf(os.Stderr, "  mow %-10s %s%s\n", c.Name, c.Summary, extra)
		}
		fmt.Fprintln(os.Stderr, "  (each pack: mow <pack> help)")
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintf(os.Stderr, `Common flags (also -long):

  --config --workspace --model --effort --base-url
  --allow-shell --allow-write --max-turns --stream --verbose
  --session --continue --no-session

Env:

  MOW_HOME                         data root (default ~/.mow)
  MOW_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY
  MOW_MODEL / OPENAI_MODEL / ANTHROPIC_MODEL
  MOW_EFFORT                       none | low | medium | high
  MOW_BASE_URL / OPENAI_BASE_URL / ANTHROPIC_BASE_URL
  MOW_WIRE                         openai-chat-completions | openai-responses | anthropic-messages
  MOW_OPS                          ops profile name (with ext/ops)
  MOW_TRUST_PROJECT=1              trust project config this run

Defaults: tools read, glob, grep. Power: --allow-write / --allow-shell.
Library: import "github.com/subosito/mow" → Engine.Prompt

`)
}
