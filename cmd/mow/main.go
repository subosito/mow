// Command mow composes the core CLI and optional runtime integrations.
//
// This file's only job is the blank-import list — drop an import and that
// subcommand or tool set disappears from this binary.
package main

import (
	"os"

	cli "github.com/subosito/mow/ext/cli"

	// Linked extensions — each registers tools/commands in init.
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/cmdhook"
	_ "github.com/subosito/mow/ext/goal"
	_ "github.com/subosito/mow/ext/mcp"
	_ "github.com/subosito/mow/ext/proc"
	_ "github.com/subosito/mow/ext/tty"
	_ "github.com/subosito/mow/ext/workflow"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
