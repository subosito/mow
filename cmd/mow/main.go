// Command mow is the lean stock binary: core CLI (ext/cli) plus the lightweight
// extension set — acp, tty, and the focus, proc, cmdhook, mcp packs. The
// heavier/specialized packs (goal, job, ops, review, media) are linked by
// cmd/mow-full instead.
//
// This file's only job is the blank-import list — drop an import and that
// subcommand disappears from this binary.
package main

import (
	"os"

	cli "github.com/subosito/mow/ext/cli"

	// Linked extensions/packs — each registers tools/commands in init.
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/tty"
	_ "github.com/subosito/mow/packs/cmdhook"
	_ "github.com/subosito/mow/packs/focus"
	_ "github.com/subosito/mow/packs/mcp"
	_ "github.com/subosito/mow/packs/proc"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
