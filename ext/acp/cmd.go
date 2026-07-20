package acp

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
	ext.RegisterBeforeNew(RegisterFromConfig)
	ext.RegisterCommand(ext.Command{
		Name:    "acp",
		Summary: "ACP agent on stdin/stdout (editors)",
		Run:     runCmd,
	})
}

func runCmd(args []string) int {
	fs := cliutil.NewFlagSet("acp")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ef.Stream = true
	eng, err := ef.NewEngine()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := Agent(ctx, AgentOptions{Engine: eng, In: os.Stdin, Out: os.Stdout}); err != nil {
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 1
	}
	return 0
}
