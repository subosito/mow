// Command mowi is the interactive TUI for the mow headless harness
// ("mow with interface").
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/packs/mowi"

	// Linked packs — each registers tools/commands in init.
	// Same set as cmd/mow so the TUI engine has acp_delegate, mcp, ops, etc.
	// otel: config-driven OTLP when otel.endpoint is set (same as stock mow).
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/cmdhook"
	_ "github.com/subosito/mow/packs/goal"
	_ "github.com/subosito/mow/packs/job"
	_ "github.com/subosito/mow/packs/lsp"
	_ "github.com/subosito/mow/ext/mcp"
	_ "github.com/subosito/mow/packs/ops"
	_ "github.com/subosito/mow/ext/proc"
	_ "github.com/subosito/mow/ext/rpc"
	_ "github.com/subosito/mow/packs/otel"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printUsage()
			return 0
		case "version", "-v", "--version":
			fmt.Println(versionString())
			return 0
		}
	}
	// Also catch -h / --help mixed with flags (flag.Parse would dump Usage of …).
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
	// Stream on by default; resume with --continue / --session.
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

func printUsage() {
	fmt.Fprintf(os.Stderr, `mowi — mow with interface (Bubble Tea TUI)

  Interactive chat over the mow harness. Agent loop, tools, sessions live in mow.

  mowi [flags]
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
  --workspace NAME|PATH  workspace root: a set name from $MOW_HOME/workspaces.yaml
                         (root + extra_roots) or a directory path
  --extra-root PATH      extra FS root for path jail (repeatable)
  --config PATH          config yaml
  -h, --help             this help
  -v, --version          print version

Config: $MOW_HOME (default ~/.mow). TUI prefs: extensions.tui in config.
  policy.extra_roots in user config (not project .mow/config).

Workspace sets ($MOW_HOME/workspaces.yaml):

  workspaces:
    monorepo:
      root: ~/code/app
      extra_roots:
        - ~/code/shared
        - ~/code/vendor:ro

  mowi --workspace monorepo      # set name → root + extra_roots
  mowi --workspace /tmp/ci       # plain directory path
`)
}
