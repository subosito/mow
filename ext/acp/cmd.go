package acp

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printUsage()
			return 0
		}
	}
	fs := cliutil.NewFlagSet("acp")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ef.Stream = true
	eng, err := ef.NewEngineCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := Agent(ctx, AgentOptions{Engine: eng, In: os.Stdin, Out: os.Stdout}); err != nil {
		// Clean EOF/cancel is normal when the editor disconnects.
		if err == context.Canceled || strings.Contains(err.Error(), "EOF") {
			return 0
		}
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow acp — Agent Client Protocol on stdio

  Point an ACP-capable editor (e.g. Zed) at this process as the agent command:
    mow acp [engine flags]

  Reads JSON-RPC on stdin, writes on stdout. Ctrl+C / SIGTERM stops the agent.

Engine flags: same as mow run (--config --model --workspace --allow-write …).
Streaming is always on for session/update chunks.

Optional: extensions.acp.agents registers the acp_delegate tool for peer harnesses.
See docs/extensions.md.

`)
}
