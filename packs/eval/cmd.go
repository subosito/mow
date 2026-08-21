// Package eval registers `mow eval` — run JSON fixture suites through the
// lightweight github.com/subosito/mow/eval harness (scripted or live model).
package eval

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/eval"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "eval",
		Summary: "Run eval/replay fixtures (scripted turns or live model)",
		Layer:   "ext",
		Run:     run,
	})
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, `mow eval — deterministic agent fixture runner

Usage:
  mow eval run FIXTURE.json [flags]

Fixture JSON is a case, a list of cases, or {"name","cases":[...]}.
Each case may include a "script" of assistant messages for deterministic
replay (no API). Omit script to use the configured live model (needs key).

Flags (run):
`)
		fs := flag.NewFlagSet("eval run", flag.ContinueOnError)
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		return 2
	}
	switch args[0] {
	case "run":
		return runRun(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mow eval: unknown subcommand %q (want run)\n", args[0])
		return 2
	}
}

func runRun(args []string) int {
	fs := flag.NewFlagSet("eval run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	timeout := fs.Duration("timeout", 10*time.Minute, "per-suite deadline")
	jsonOut := fs.Bool("json", false, "print suite report as JSON")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "mow eval run: FIXTURE.json required")
		return 2
	}
	path := fs.Arg(0)
	fix, err := eval.LoadFixture(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow eval: %v\n", err)
		return 1
	}

	base := ef.Options()
	base.NoSession = true
	opt := eval.Options{
		Base:       base,
		Workspace:  base.Workspace,
		AllowWrite: base.AllowWrite,
		AllowShell: base.AllowShell,
		MaxTurns:   base.MaxTurns,
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	sr := eval.RunFixture(ctx, fix, opt)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(sr)
	} else {
		for _, r := range sr.Reports {
			status := "ok"
			if !r.OK {
				status = "FAIL"
			}
			fmt.Printf("%s  %s", status, r.Name)
			if r.Err != "" {
				fmt.Printf("  err=%s", r.Err)
			}
			if len(r.Failures) > 0 {
				fmt.Printf("  %v", r.Failures)
			}
			fmt.Println()
		}
		fmt.Printf("passed=%d failed=%d ok=%v\n", sr.Passed, sr.Failed, sr.OK)
	}
	if !sr.OK {
		return 1
	}
	return 0
}
