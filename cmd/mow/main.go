// Command mow is a thin shell: core commands (run, repl) plus whatever
// extension packs are blank-imported below. Packs own their subcommands via
// ext.RegisterCommand — drop an import and the subcommand disappears.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"

	// Linked packs — each registers tools/commands in init.
	// Remove an import to drop that pack (and its subcommand) from this binary.
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/goal"
	_ "github.com/subosito/mow/ext/lsp"
	_ "github.com/subosito/mow/ext/mcp"
	_ "github.com/subosito/mow/ext/rpc"
	_ "github.com/subosito/mow/ext/schedule"
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
	cands := []string{"run", "repl", "version", "help"}
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
		return 2
	}
	opt := ef.Options()
	if ef.Stream {
		opt.OnToken = func(d string) { fmt.Fprint(os.Stderr, d) }
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := mow.Run(ctx, prompt, opt)
	if err != nil {
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

func replCmd(args []string) int {
	fs := cliutil.NewFlagSet("repl")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opt := ef.Options()
	if ef.Stream {
		opt.Stream = true
		opt.OnToken = func(d string) { fmt.Fprint(os.Stderr, d) }
	}
	eng, err := mow.New(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow repl: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "mow repl — empty line or /quit to exit")
	if ef.Stream {
		fmt.Fprintln(os.Stderr, "(token stream on stderr via --stream)")
	}
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
		if ef.Stream {
			fmt.Fprint(os.Stderr, "\n")
		}
		res, err := eng.Prompt(ctx, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow: %v\n", err)
		}
		if res.Text != "" {
			if ef.Stream {
				fmt.Fprint(os.Stderr, "\n")
			}
			fmt.Println(res.Text)
		}
	}
	return 0
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow — agentic harness (library + CLI)

API:
  import "github.com/subosito/mow"
  eng, _ := mow.New(mow.Options{...})
  res, _ := eng.Prompt(ctx, "…")

Core:
  mow run -p "prompt" [flags]   one-shot
  mow repl [flags]              line REPL
  mow version | help

`)
	if cmds := ext.Commands(); len(cmds) > 0 {
		fmt.Fprintln(os.Stderr, "Extensions (linked packs):")
		for _, c := range cmds {
			extra := ""
			if c.DefaultInteractive {
				extra = "  [default on TTY]"
			}
			fmt.Fprintf(os.Stderr, "  mow %-12s %s%s\n", c.Name, c.Summary, extra)
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintf(os.Stderr, `Shared flags (help shows --long; -long also works):
  --config --workspace --model --base-url
  --allow-shell --allow-write --max-turns --stream
  --session --continue --no-session

Env (supported):
  MOW_HOME                         user data root (default ~/.mow)
  MOW_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY
  MOW_MODEL / OPENAI_MODEL / ANTHROPIC_MODEL
  MOW_BASE_URL / OPENAI_BASE_URL / ANTHROPIC_BASE_URL
  MOW_WIRE                         openai-chat-completions | anthropic-messages
  MOW_TRUST_PROJECT=1              trust project .mow/config (or create .mow/trust)

Secure default tools: read, glob, grep. Power tools: --allow-write / --allow-shell.

`)
}
