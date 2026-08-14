package rpc

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/subosito/mow"
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
	// NOT NewEngineCLI: that installs the stderr tool-progress printer
	// ("→ bash …"), which a TUI host inherits and which lands on the terminal
	// outside its frame, corrupting the display. Tool activity reaches the
	// client as event notifications on stdout instead.
	eng, err := mow.New(ef.Options())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow rpc: %v\n", err)
		return 1
	}
	defer eng.Close() // tear down session cleanups (e.g. proc_start keep=false)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &Server{Engine: eng, In: os.Stdin, Out: os.Stdout}
	if err := srv.Serve(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "mow rpc: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow rpc — JSON-lines control plane on stdio

  One JSON object per line on stdin; responses and events on stdout.

  mow rpc [engine flags]

Methods:

  prompt      {"id":1,"method":"prompt","params":{"text":"…"}}
  cancel      {"id":2,"method":"cancel"}
  status      {"id":3,"method":"status"}
  session     {"id":4,"method":"session"}
  version     {"id":5,"method":"version"}
  capabilities {"id":6,"method":"capabilities"}  (methods, controls, optional linked features)
  ping        {"id":6,"method":"ping"}
  extension.config {"id":7,"method":"extension.config","params":{"name":"mowi"}}

Host methods (for an external UI):

  sessions    {"id":7,"method":"sessions"}
  transcript  {"id":8,"method":"transcript"}
  steer       {"id":9,"method":"steer","params":{"text":"…"}}
  slash.list  {"id":10,"method":"slash.list"}
  slash       {"id":11,"method":"slash","params":{"name":"review","args":[]}}
  perm.set    {"id":12,"method":"perm.set","params":{"mode":"ask"}}
  perm.decide  model.list  model.set  effort.list  effort.set
    context  compact  rewind  skill.list  skill.activate {"id":13,"method":"perm.decide","params":{"id":"perm-1","decision":"allow"}}

During prompt, unsolicited events may appear (no id):
  {"method":"event","params":{"type":"loop.token"|"harness.tool.start"|…}}

In ask mode, write/edit/bash pause and emit:
  {"method":"perm.ask","params":{"id":"perm-1","name":"write","args":{…}}}

tool.end includes duration_ms. Cancel/status stay responsive while a prompt runs.

Engine flags: same as mow run. Docs: docs/extensions.md

`)
}
