package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterBeforeNew(RegisterFromConfig)
	ext.RegisterOptionalFeature(ext.OptionalFeature{
		ID:     "acp",
		Events: []string{"harness.delegate.chunk", "harness.delegate.progress", "harness.delegate.usage"},
	})
	ext.RegisterCommand(ext.Command{
		Name:    "acp",
		Summary: "ACP agent on stdin/stdout",
		Layer:   "ext",
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
	if err := ef.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 2
	}
	// Plain Options, not OptionsCLI: tokens and tool progress go to the editor
	// over ACP, so the stderr printers would only duplicate them into its log.
	cliutil.EnableVerbose(ef.Verbose)
	opt := ef.Options()
	opt.Stream = true
	eng, err := mow.New(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 1
	}
	defer eng.Close() // drop delegate peers
	ctx, stop := signal.NotifyContext(context.Background(), acpStopSignals()...)
	defer stop()
	if err := Agent(ctx, AgentOptions{Engine: eng, In: os.Stdin, Out: os.Stdout}); err != nil {
		// Clean EOF/cancel is normal when the editor disconnects.
		if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "EOF") {
			return 0
		}
		fmt.Fprintf(os.Stderr, "mow acp: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow acp — Agent Client Protocol on stdio

  mow acp [engine flags]

  Config: mode (ask|code), approvals (prompt|always), model, effort.
  Peers:  extensions.acp.peers → delegate.

Engine flags: same as mow run.

`)
}
