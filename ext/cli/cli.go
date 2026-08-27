// Package cli is the unix CLI skeleton for mow host binaries: core subcommands
// (run, trust, doctor, approvals, version, help) plus dispatch for whatever
// extensions/packs the embedding main blank-imports.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "run",
		Summary: "one-shot prompt",
		Layer:   "ext",
		Run:     runCmd,
	})
	ext.RegisterCommand(ext.Command{
		Name:    "trust",
		Summary: "trust this workspace",
		Layer:   "ext",
		Run:     func(args []string) int { return cliutil.TrustCommand("mow", args) },
	})
	ext.RegisterCommand(ext.Command{
		Name:    "doctor",
		Summary: "inspect host/workspace",
		Layer:   "ext",
		Run:     doctorCmd,
	})
	ext.RegisterCommand(ext.Command{
		Name:    "approvals",
		Summary: "durable tool approvals",
		Layer:   "ext",
		Run:     approvalsCmd,
	})
	ext.RegisterCommand(ext.Command{
		Name:    "version",
		Summary: "print version",
		Layer:   "ext",
		Run: func(args []string) int {
			fmt.Println(mow.VersionString())
			return 0
		},
	})
	ext.RegisterCommand(ext.Command{
		Name:    "help",
		Summary: "usage and command help",
		Layer:   "ext",
		Run:     helpCmd,
	})
}

// Main runs the CLI over args (os.Args[1:]) and returns the exit status.
func Main(args []string) int {
	return dispatch(args)
}

var coreCommandNames = map[string]bool{
	"run": true, "trust": true, "doctor": true, "approvals": true,
	"models": true, "version": true, "help": true,
}

func dispatch(args []string) int {
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
	case "version", "-v", "--version":
		fmt.Println(mow.VersionString())
		return 0
	case "help", "-h", "--help":
		if len(args) == 1 {
			printUsage()
			return 0
		}
		return helpCmd(args[1:])
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

// helpCmd routes `mow help <command>` to the same command-specific help
// users get from `mow <command> help`.
func helpCmd(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}
	if c, ok := ext.LookupCommand(args[0]); ok {
		return c.Run(append([]string{"help"}, args[1:]...))
	}
	fmt.Fprintf(os.Stderr, "mow help: unknown command %q\n", args[0])
	fmt.Fprintln(os.Stderr, "  run `mow help` to list available commands")
	return 2
}

func suggestCommand(name string) string {
	cands := []string{"run", "trust", "doctor", "approvals", "version", "help"}
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
	case "run", "tty", "trust", "doctor", "approvals", "models", "version", "help",
		"rpc", "ops", "repl", "acp", "goal", "review", "sec", "job", "proc",
		"mcp", "media", "focus", "plugin":
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	eng, err := mow.NewHarness(ef.OptionsCLI())
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

func printRunUsage() {
	fmt.Fprintf(os.Stderr, `mow run — one-shot prompt

  mow run -p "…" [flags]
  mow run "free-form prompt text" [flags]

  -p TEXT              prompt (or pass as args)
  -e, --ephemeral      do not save this turn
  --config --workspace --model --effort --base-url --extra-root
  --allow-shell --allow-write --sandbox (Linux) --max-turns
  --stream --verbose --session --continue --no-session

  mow run -p "summarize this repo"
  mow run -p "fix the tests" --allow-write --allow-shell

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
	fmt.Fprintln(os.Stderr)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow — agent harness

Core:

  mow run  -p "…"             one-shot prompt
  mow models [filter]         list catalog models
  mow trust [path]            trust this workspace
  mow doctor [--bundle]       inspect host/workspace
  mow approvals               durable tool approvals
  mow version | help

`)
	if cmds := ext.Commands(); len(cmds) > 0 {
		var extensions, packs []ext.Command
		for _, c := range cmds {
			if coreCommandNames[c.Name] {
				continue
			}
			if strings.EqualFold(c.Layer, "pack") {
				packs = append(packs, c)
			} else {
				extensions = append(extensions, c)
			}
		}
		printCmdGroup("Extensions (this binary):", extensions)
		printCmdGroup("Packs (this binary):", preferFirst(packs, "mcp"))
	}
	fmt.Fprintf(os.Stderr, `Flags: --config --workspace --model --effort --base-url --extra-root
       --allow-shell --allow-write --sandbox --max-turns --stream --verbose
       --session --continue --no-session

Env:   MOW_HOME  MOW_API_KEY  MOW_MODEL  MOW_BASE_URL  MOW_WIRE  MOW_EFFORT%s

`, packEnvHelp())
}

// preferFirst puts the named command first and keeps the rest in order.
func preferFirst(cmds []ext.Command, name string) []ext.Command {
	var first, rest []ext.Command
	for _, c := range cmds {
		if c.Name == name {
			first = append(first, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(first, rest...)
}

// packEnvHelp is env that only exists when the matching pack is linked
// (cmd/mowx, not the lean cmd/mow).
func packEnvHelp() string {
	if _, ok := ext.LookupCommand("ops"); ok {
		return "  MOW_OPS"
	}
	return ""
}
