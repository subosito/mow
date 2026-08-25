// Command mow is the lean stock binary: core commands (run, tty, trust,
// doctor, approvals, version, help) plus the lightweight extension set —
// acp and rpc extensions, and the focus, proc, cmdhook, mcp packs. The
// heavier/specialized packs (goal, job, ops, review, media) are linked by
// cmd/mow-full instead.
//
// The CLI itself lives in cmd/internal/mowcli; this file's only job is the
// blank-import list — drop an import and that subcommand disappears from
// this binary.
package main

import (
	"os"

	"github.com/subosito/mow/cmd/internal/mowcli"

	// Linked extensions/packs — each registers tools/commands in init.
	_ "github.com/subosito/mow/ext/acp"
	_ "github.com/subosito/mow/ext/rpc"
	_ "github.com/subosito/mow/packs/cmdhook"
	_ "github.com/subosito/mow/packs/focus"
	_ "github.com/subosito/mow/packs/mcp"
	_ "github.com/subosito/mow/packs/proc"
)

func main() {
	os.Exit(mowcli.Main(os.Args[1:]))
}
