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
	fs := cliutil.NewFlagSet("rpc")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	eng, err := ef.NewEngine()
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
