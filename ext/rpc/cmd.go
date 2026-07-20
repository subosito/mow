package rpc

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "rpc",
		Summary: "JSON-lines RPC on stdin/stdout",
		Run:     runCmd,
	})
}

func runCmd(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printUsage()
			return 0
		}
	}
	fs := cliutil.NewFlagSet("rpc")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	eng, err := ef.NewEngineCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow rpc: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &Server{Engine: eng, In: os.Stdin, Out: os.Stdout}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mow rpc: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow rpc — JSON-lines control plane on stdio

  One JSON object per line on stdin; responses and event notifications on stdout.

Methods:
  prompt   {"id":1,"method":"prompt","params":{"text":"…"}}
  cancel   {"id":2,"method":"cancel"}
  status   {"id":3,"method":"status"}
  session  {"id":4,"method":"session"}
  version  {"id":5,"method":"version"}
  ping     {"id":6,"method":"ping"}

During prompt, unsolicited events may appear (no id):
  {"method":"event","params":{"type":"token"|"tool.start"|"tool.end"|…}}

tool.end includes duration_ms. Cancel/status stay responsive while a prompt runs.

  mow rpc [engine flags]

Engine flags: same as mow run. See docs/extensions.md.

`)
}
