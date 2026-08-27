// Package review also provides the `mow review` and `mow sec` subcommands.
// Both run the same workflow. Profile (general vs security) is an internal
// implementation detail: the public surface is the two commands, not a flag.
package review

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "review",
		Summary: "advisory code review",
		Layer:   "pack",
		Run:     func(args []string) int { return runCommand("review", args) },
	})
	ext.RegisterCommand(ext.Command{
		Name:    "sec",
		Summary: "advisory security review",
		Layer:   "pack",
		Run:     func(args []string) int { return runCommand("sec", args) },
	})
}

// wantsHelp reports whether args is an explicit help request.
//
// It stops at "--" and only accepts the bare word "help" in first position, so
// a path or flag value that happens to spell a help token is still reviewed:
// `mow review -- --help` must review a file named "--help", not print usage
// and exit 0 (which in CI would read as a clean review).
func wantsHelp(args []string) bool {
	for i, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
		if a == "help" && i == 0 {
			return true
		}
	}
	return false
}

// fail reports err on stderr under the invoked command's name and returns
// ExitError. Library errors carry a "review:" package prefix; printing it after
// "mow sec: " would read as the wrong command, so it is trimmed here — the
// prefix is for Go callers, not for the user's terminal.
func fail(cmd string, err error) int {
	msg := strings.TrimPrefix(err.Error(), "review: ")
	fmt.Fprintf(os.Stderr, "mow %s: %s\n", cmd, msg)
	return ExitError
}

// runCommand is the shared entry point. cmd is "review" or "sec"; that alone
// selects the internal profile (general vs security).
func runCommand(cmd string, args []string) int {
	// The command name is the product surface, so map it to a persona
	// explicitly: a future subcommand that forgets to register one must fail
	// loudly rather than silently inherit the general review.
	prof, profErr := profileFor(cmd)
	if profErr != nil {
		fmt.Fprintf(os.Stderr, "mow %s: %v\n", cmd, profErr)
		return ExitError
	}
	if wantsHelp(args) {
		printUsage(cmd)
		return ExitClean
	}
	fs := cliutil.NewFlagSet("mow " + cmd)
	fs.Usage = func() { printUsage(cmd) }

	var rf CLIFlags
	rf.Bind(fs)
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	paths, err := parseArgs(fs, args)
	if err != nil {
		// flag.Parse already printed usage for an explicit help request
		// (including the stdlib "-help" spelling); that is not a failure.
		if errors.Is(err, flag.ErrHelp) {
			return ExitClean
		}
		return fail(cmd, err)
	}

	workspace, err := resolveWorkspace(ef.Workspace)
	if err != nil {
		return fail(cmd, err)
	}
	// ExitPolicy is carried on req and stamped onto Report.Exit inside Run.
	req, format, _, err := rf.Resolve(prof, workspace, paths)
	if err != nil {
		return fail(cmd, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Load extensions.review before resolving scope. Budgets are config-
	// tunable, and ResolveScope enforces them — deferring this to mow.New
	// would resolve the scope against the built-in caps while review engines
	// skip extension BeforeNew (SkipExtensionSetup), so budgets must load here.
	if err := loadConfig(ef.ConfigPaths()...); err != nil {
		return fail(cmd, err)
	}

	// Resolve scope before building an engine: a bad selector or an empty
	// scope should not cost a model connection.
	sc, err := ResolveScope(ctx, req.Scope)
	if err != nil {
		return fail(cmd, err)
	}
	if !rf.Quiet {
		fmt.Fprintf(os.Stderr, "mow %s: %s — %d file(s) in scope", cmd, scopeModeDescription(sc), len(sc.Files))
		if sc.Truncated {
			fmt.Fprintf(os.Stderr, " (truncated: %s)", sc.TruncReason)
		}
		fmt.Fprintln(os.Stderr)
	}
	// An empty scope is reported even under --quiet: the run produces a
	// findings-free report and exits 0, so a typo'd selector would otherwise
	// look exactly like a clean review in CI.
	if sc.Empty() {
		fmt.Fprintf(os.Stderr, "mow %s: warning: no files in scope — nothing was reviewed "+
			"(check the selector, --exclude patterns, or --budget)\n", cmd)
	}

	models, err := reviewerModels(rf.Reviewers)
	if err != nil {
		return fail(cmd, err)
	}

	// A review must never modify the code it reviews, regardless of config.
	ef.AllowWrite = false
	ef.AllowShell = false
	// Review output is the artifact; the conversation is not worth persisting.
	ef.NoSession = true
	// Never stream raw model tokens: the passes speak JSON, and dumping a
	// half-formed envelope on stderr would look like output while being
	// unusable. Progress is reported as tool lines instead.
	ef.Stream = false
	opt := ef.OptionsCLI()
	opt.Workspace = workspace
	// A review reads a lot of files and takes minutes; without progress it
	// looks hung. --quiet keeps stderr clean for scripted use.
	if rf.Quiet {
		opt.OnEvent = nil
	}
	if budget, ok := LookupBudget(rf.Budget); ok && !ef.MaxTurnsSet {
		opt.MaxTurns = budget.MaxTurns
	}
	applyReviewEngineIsolation(&opt)

	var reviewer Reviewer
	var engines []*mow.Engine
	if len(models) == 0 {
		eng, err := mow.New(opt)
		if err != nil {
			return fail(cmd, err)
		}
		engines = []*mow.Engine{eng}
		reviewer = NewEngineReviewer(eng)
	} else {
		budget, _ := LookupBudget(rf.Budget)
		opts, parallel, err := ensembleOptions(ef, workspace, models, rf.ReviewerParallel, rf.Quiet, budget)
		if err != nil {
			return fail(cmd, err)
		}
		engines, err = newEnsembleEngines(opts, mow.New)
		if err != nil {
			return fail(cmd, err)
		}
		ensemble, err := ensembleFromEngines(models, engines, parallel)
		if err != nil {
			closeEngines(engines)
			return fail(cmd, err)
		}
		reviewer = ensemble
	}
	if verifierModel := strings.TrimSpace(rf.VerifierModel); verifierModel != "" {
		budget, _ := LookupBudget(rf.Budget)
		vopt := verifierEngineOptions(ef, workspace, verifierModel, rf.Quiet, budget)
		verEng, err := mow.New(vopt)
		if err != nil {
			closeEngines(engines)
			return fail(cmd, err)
		}
		engines = append(engines, verEng)
		req.Verifier = NewEngineReviewer(verEng)
	}
	defer closeEngines(engines)

	if !rf.Quiet && !sc.Empty() {
		passes := "2 passes"
		if rf.NoVerify {
			passes = "1 pass (verification skipped)"
		}
		name := engines[0].Model()
		if len(models) > 0 {
			name = strings.Join(models, ", ")
		}
		if req.Verifier != nil {
			fmt.Fprintf(os.Stderr, "mow %s: reviewing with %s, %s (verifier: %s)…\n", cmd, name, passes, req.Verifier.Model())
		} else {
			fmt.Fprintf(os.Stderr, "mow %s: reviewing with %s, %s…\n", cmd, name, passes)
		}
	}

	res, err := Run(ctx, reviewer, sc, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			fmt.Fprintf(os.Stderr, "mow %s: cancelled\n", cmd)
			return ExitError
		}
		return fail(cmd, err)
	}

	if err := emit(res.Report, format, rf, ef.Verbose); err != nil {
		return fail(cmd, err)
	}
	return res.Report.Exit.Code
}

// emit writes the report to stdout or --output.
//
// Color is opt-in on a TTY only, and never when writing to a file: a report
// committed to CI artifacts or piped into another tool must not carry escape
// codes.
//
// A file report is written atomically (temp file in the same directory, then
// rename). os.Create truncates on open, so rendering straight to the target
// would destroy a previous good report and leave a truncated artifact behind
// if rendering failed — a CI job publishing that path would pick up the
// half-written file even though the command exited non-zero.
func emit(rep *Report, format Format, rf CLIFlags, verbose bool) error {
	topt := TextOptions{
		Color:   !rf.NoColor && rf.Output == "" && isTTY(os.Stdout),
		Verbose: verbose,
	}
	path := strings.TrimSpace(rf.Output)
	if path == "" {
		return Render(os.Stdout, rep, format, topt)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mow-report-*")
	if err != nil {
		return fmt.Errorf("open --output: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if err := Render(tmp, rep, format, topt); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write --output: %w", err)
	}
	// Reports are meant to be read by other tools; keep them world-readable
	// rather than inheriting CreateTemp's 0600.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write --output: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write --output: %w", err)
	}
	if !rf.Quiet {
		fmt.Fprintf(os.Stderr, "report written to %s (%s)\n", path, outcomeSummary(rep))
	}
	return nil
}

// outcomeSummary describes a finished report in one line.
//
// "0 finding(s)" is ambiguous in the one way that matters: it reads as a clean
// review whether the model found nothing, found things that verification
// rejected, or found things filtered out by --min-severity. Those call for
// completely different next steps, and with --format sarif the operator never
// sees the report body that would have explained it. Say which one happened.
func outcomeSummary(rep *Report) string {
	if rep == nil {
		return "no report"
	}
	out := fmt.Sprintf("%d finding(s)", rep.Counts.Total)
	if rep.Suppressed > 0 {
		out += fmt.Sprintf(", %d suppressed", rep.Suppressed)
	}
	if rep.Counts.Total == 0 && rep.Suppressed == 0 && rep.Scope.FilesReviewed > 0 {
		// Nothing found and nothing dropped: the passes genuinely reported a
		// clean read. Worth stating, because it is the only case where "0"
		// means what it appears to mean.
		out += " — nothing reported by either pass"
	}
	return out
}

// resolveWorkspace defaults to the current directory.
func resolveWorkspace(flagValue string) (string, error) {
	ws := strings.TrimSpace(flagValue)
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return cwd, nil
	}
	return filepath.Abs(ws)
}

// isTTY reports whether f is a terminal (for color defaults).
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
