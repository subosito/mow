package review

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/subosito/mow/slash"
)

// Interactive `/review` and `/sec`. Registering here is what makes the two
// commands appear in every interactive host (`mow tty`, the Rust mowi TUI over
// `mow acp`) purely
// because this pack is linked: no host names the review pack, and dropping the
// blank import removes the commands from both hosts at once.
//
// The flag surface is the CLI's, parsed by the same CLIFlags — one grammar to
// learn, and `--staged` cannot mean different things in two places. What the
// slash path deliberately does not take is the engine flags: it runs against
// the session's live engine, so the review uses the model you are already
// talking to.
func init() {
	slash.Register(slash.Command{
		Name:      "review",
		Summary:   "AI-assisted code review of a diff or paths (advisory)",
		Usage:     slashUsage("review"),
		Exclusive: true,
		Run:       func(ctx context.Context, req slash.Request) (slash.Result, error) { return runSlash(ctx, req) },
	})
	slash.Register(slash.Command{
		Name:      "sec",
		Summary:   "AI-assisted security review of a diff or paths (advisory)",
		Usage:     slashUsage("sec"),
		Exclusive: true,
		Run:       func(ctx context.Context, req slash.Request) (slash.Result, error) { return runSlash(ctx, req) },
	})
}

// runSlash is the shared body of the interactive commands. It mirrors
// runCommand's middle (profile → flags → scope → workflow) but stops before
// process concerns: no exit codes, no os.Stdout, no signal handling. Those
// belong to the CLI; a slash command returns text and lets the host paint it.
func runSlash(ctx context.Context, req slash.Request) (slash.Result, error) {
	cmd := req.Name
	prof, err := profileFor(cmd)
	if err != nil {
		return slash.Result{}, err
	}
	if slash.IsHelpArgs(req.Args) {
		return slash.Result{Title: "/" + cmd + " · usage", Body: slashUsage(cmd)}, nil
	}

	// Parse the CLI flag surface, minus engine flags. flag prints to its
	// output on error; send that to a buffer so a typo does not scribble over
	// a TUI frame, and fold the message into the returned error instead.
	fs := flag.NewFlagSet("/"+cmd, flag.ContinueOnError)
	var usage bytes.Buffer
	fs.SetOutput(&usage)
	fs.Usage = func() {}
	var rf CLIFlags
	rf.Bind(fs)
	if err := fs.Parse(req.Args); err != nil {
		return slash.Result{}, fmt.Errorf("/%s: %w\n\n%s", cmd, err, slashUsage(cmd))
	}

	if req.Engine == nil {
		return slash.Result{}, fmt.Errorf("/%s: no engine in this session", cmd)
	}

	ws := req.Workspace
	if ws == "" {
		ws = req.Engine.Workspace()
	}
	// Format and exit policy are CLI concerns: an interactive host always
	// renders text into its own transcript, and there is no process to exit.
	rq, _, _, err := rf.Resolve(prof, ws, fs.Args())
	if err != nil {
		return slash.Result{}, fmt.Errorf("/%s: %w", cmd, err)
	}

	// Budget overrides are already installed by BeforeNew when the host built
	// its engine, so unlike the CLI path this does not reload config — the
	// session's engine and this scope see the same caps either way.
	sc, err := ResolveScope(ctx, rq.Scope)
	if err != nil {
		return slash.Result{}, fmt.Errorf("/%s: %w", cmd, err)
	}
	// An empty scope is a successful empty review, not an error — same rule as
	// the CLI. Saying "nothing was reviewed" out loud matters more here than
	// in CI, because there is no exit code to notice.
	if sc.Empty() {
		return slash.Result{
			Title: cmd + " · no files in scope — nothing was reviewed",
			Body:  "check the selector, --exclude, or --budget",
		}, nil
	}

	res, err := Run(ctx, NewEngineReviewer(req.Engine), sc, rq)
	if err != nil {
		return slash.Result{}, fmt.Errorf("/%s: %w", cmd, err)
	}

	var buf bytes.Buffer
	if err := RenderText(&buf, res.Report, TextOptions{Color: req.Color}); err != nil {
		return slash.Result{}, fmt.Errorf("/%s: %w", cmd, err)
	}
	return slash.Result{
		Title: SlashSummary(cmd, res.Report),
		Body:  strings.TrimRight(buf.String(), "\n"),
	}, nil
}

// profileFor maps a command name to its persona. Unknown names fail loudly
// rather than inheriting the general review — same reasoning as runCommand.
func profileFor(cmd string) (*Profile, error) {
	switch cmd {
	case "review":
		return GeneralProfile(), nil
	case "sec":
		return SecurityProfile(), nil
	default:
		return nil, fmt.Errorf("no review profile registered for %q", cmd)
	}
}

// SlashSummary is the one-line status for a finished report: the line a TUI
// puts in a status chip and a plain terminal prints above the body. Exported
// because hosts that render the report themselves still want the same wording.
func SlashSummary(cmd string, rep *Report) string {
	if rep == nil {
		return cmd + " · report"
	}
	n := len(rep.Findings)
	out := fmt.Sprintf("%s · report · %d finding", cmd, n)
	if n != 1 {
		out += "s"
	}
	if extra := SlashCounts(rep); extra != "" && n > 0 {
		out += " · " + extra
	}
	return out
}

// SlashCounts renders the severity breakdown as "high 2 · low 1".
func SlashCounts(rep *Report) string {
	if rep == nil {
		return ""
	}
	c := rep.Counts
	var parts []string
	add := func(label string, n int) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", label, n))
		}
	}
	add("critical", c.Critical)
	add("high", c.High)
	add("medium", c.Medium)
	add("low", c.Low)
	add("info", c.Info)
	if len(parts) > 0 {
		if rep.Suppressed > 0 {
			parts = append(parts, fmt.Sprintf("suppressed %d", rep.Suppressed))
		}
		return strings.Join(parts, " · ")
	}
	if n := len(rep.Findings); n > 0 {
		return fmt.Sprintf("%d total", n)
	}
	if rep.Suppressed > 0 {
		return fmt.Sprintf("suppressed %d", rep.Suppressed)
	}
	return "none"
}

func slashUsage(cmd string) string {
	return strings.TrimSpace(fmt.Sprintf(`/%s [paths…] [flags] — same workflow as mow %s

  scope:   --diff main...HEAD | --staged | --base origin/main | paths…
  budget:  --budget small|medium|large
  filter:  --min-severity high | --include-low | --include-unverified
  other:   --no-verify (skip verification pass)

  default scope: uncommitted changes, or the whole tree if clean
  runs against the session's current model; cancel with esc / ctrl+c`, cmd, cmd))
}
