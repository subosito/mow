// Command mowx is the full stock binary: everything cmd/mow links
// (acp, tty, focus, proc, cmdhook, mcp) plus the remaining packs — goal,
// job, ops, review, media.
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
	_ "github.com/subosito/mow/packs/goal"
	_ "github.com/subosito/mow/packs/job"
	_ "github.com/subosito/mow/packs/mcp"
	_ "github.com/subosito/mow/packs/media"
	_ "github.com/subosito/mow/packs/ops"
	_ "github.com/subosito/mow/packs/proc"
	_ "github.com/subosito/mow/packs/review"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
