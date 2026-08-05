// Command mowi is the interactive TUI for the mow headless harness
// ("mow with interface"). It also dispatches pack subcommands (acp, ops,
// goal, review, …) just like cmd/mow, so a single binary serves both the
// interactive TUI and the headless subcommand surface.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/packs/mowi"

	// Linked packs — each registers tools/commands in init.
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/cmdhook"
	_ "github.com/subosito/mow/ext/eval"
	_ "github.com/subosito/mow/ext/mcp"
	_ "github.com/subosito/mow/ext/proc"
	_ "github.com/subosito/mow/ext/rpc"
	_ "github.com/subosito/mow/packs/goal"
	_ "github.com/subosito/mow/packs/job"
	_ "github.com/subosito/mow/packs/lsp"
	_ "github.com/subosito/mow/packs/ops"
	_ "github.com/subosito/mow/packs/otel"
	_ "github.com/subosito/mow/packs/review"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return startTUI(args)
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage()
		return 0
	case "version", "-v", "--version":
		fmt.Println(versionString())
		return 0
	}
	// Pack-registered subcommands (acp, ops, goal, review, mcp, …).
	if c, ok := ext.LookupCommand(args[0]); ok {
		return c.Run(args[1:])
	}
	// Flag-style args (no subcommand): start the TUI with flags.
	if strings.HasPrefix(args[0], "-") {
		return startTUI(args)
	}
	// Unknown: suggest the closest subcommand.
	fmt.Fprintf(os.Stderr, "mowi: unknown command %q\n", args[0])
	suggestSubcommand(args[0])
	fmt.Fprintln(os.Stderr, "  run `mowi help` to list available commands")
	return 2
}

// startTUI parses TUI flags and launches the interactive session.
func startTUI(args []string) int {
	// Also catch -h / --help mixed with flags.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printUsage()
			return 0
		}
		if a == "-v" || a == "--version" {
			fmt.Println(versionString())
			return 0
		}
	}

	fs := cliutil.NewFlagSet("mowi")
	fs.Usage = printUsage
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	ask := fs.Bool("ask", false, "prompt before write/edit/bash (default when --allow-write/--allow-shell)")
	auto := fs.Bool("auto", false, "run power tools without prompting (opt out of the ask default)")
	noStream := fs.Bool("no-stream", false, "disable live token streaming")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "mowi: unexpected args %q (this is a TUI — no free-form prompt CLI)\n", strings.Join(rest, " "))
		fmt.Fprintln(os.Stderr, "  mowi help")
		return 2
	}
	ef.Stream = !*noStream
	eng, err := ef.NewEngine()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mowi: %v\n", err)
		return 1
	}
	// Elevated power defaults to ask: --allow-shell without --auto must not
	// execute commands with zero interaction.
	askPerm := *ask || ((ef.AllowWrite || ef.AllowShell) && !*auto)
	if err := mowi.RunOpts(mowi.Options{
		Engine:         eng,
		AskPermissions: askPerm,
		DisableStream:  *noStream,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "mowi: %v\n", err)
		return 1
	}
	return 0
}

func versionString() string {
	// Prefer harness version (shared module); label as mowi host.
	s := mow.VersionString()
	if strings.HasPrefix(s, "mow ") {
		return "mowi (" + s + ")"
	}
	return "mowi " + s
}

// suggestSubcommand prints a "did you mean" hint for a close command name.
func suggestSubcommand(name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return
	}
	cands := []string{"help", "version"}
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
		fmt.Fprintf(os.Stderr, "did you mean %q?\n", best)
	}
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

func printUsage() {
	// List pack-registered subcommands.
	var cmds []string
	for _, c := range ext.Commands() {
		cmds = append(cmds, c.Name)
	}
	cmdsStr := "  (none)"
	if len(cmds) > 0 {
		cmdsStr = "  " + strings.Join(cmds, ", ")
	}

	fmt.Fprint(os.Stderr, `mowi — mow with interface (Bubble Tea TUI)

  Interactive chat over the mow harness. Agent loop, tools, sessions live in mow.
  Pack subcommands (acp, ops, goal, review, mcp, …) work the same as `+"`mow`"+`.

  mowi [flags]             start interactive TUI
  mowi <command> [args]    run a pack subcommand (same surface as mow)
  mowi help | version

Session:

  (default)              new session
  --continue             resume latest
  --session ID           resume by id

Permissions (write/edit/bash and other power tools):

  --allow-write          enable; prompt y/n/a before each (default ask)
  --allow-shell          enable shell; prompt before each (default ask)
  --ask                  force prompts
  --auto                 no prompts (opt out of ask)

Other:

  --no-stream            disable live token streaming
  --model NAME           override model
  --config PATH          config yaml
  -h, --help             this help
  -v, --version          print version

Available commands:
`+cmdsStr+`

Config: $MOW_HOME (default ~/.mow). TUI prefs: extensions.tui in config.
`)
}
