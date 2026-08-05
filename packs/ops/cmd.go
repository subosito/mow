package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/packs/job"
	"unicode/utf8"
)

// opsCmd is the mow ops entrypoint.
//
//	mow ops list
//	mow ops show NAME
//	mow ops check NAME
//	mow ops services NAME
//	mow ops status NAME [service]
//	mow ops incidents NAME
//	mow ops run NAME [--every 5m|--once] [engine flags]
func opsCmd(args []string) int {
	opsFlag, rest := peelOpsFlag(args)
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		printOpsUsage()
		return 0
	}
	switch rest[0] {
	case "list", "profiles", "ls":
		return cmdListProfiles(rest[1:])
	case "show", "info":
		return cmdShow(opsFlag, rest[1:])
	case "check", "validate":
		return cmdCheck(opsFlag, rest[1:])
	case "services":
		return cmdServices(opsFlag, rest[1:])
	case "status":
		return cmdStatus(opsFlag, rest[1:])
	case "incidents":
		return cmdIncidents(opsFlag, rest[1:])
	case "run", "serve", "watch":
		return cmdRun(opsFlag, rest[1:])
	default:
		// `mow ops NAME` alone → show help pointing at run
		if !strings.HasPrefix(rest[0], "-") {
			fmt.Fprintf(os.Stderr, "mow ops: unknown command %q (profile names go after the verb)\n\n", rest[0])
			fmt.Fprintf(os.Stderr, "  mow ops list\n  mow ops show %s\n  mow ops run %s\n  mow ops status %s\n", rest[0], rest[0], rest[0])
			return 2
		}
		fmt.Fprintf(os.Stderr, "mow ops: unknown %q\n", rest[0])
		printOpsUsage()
		return 2
	}
}

func printOpsUsage() {
	fmt.Fprintf(os.Stderr, `mow ops — continuous fleet monitor + remediate

Profiles: $MOW_HOME/ops/<name>/
  config.yaml   services, actions, acp peers, model, every, prompt
  prompt.md     system instructions (how to fix this stack)
  incidents/    durable work items (open → fix → close)

Each mow ops run tick: scan logs/status → ticket real issues → restart and/or
acp_delegate peers to fix code → update/close incidents. Not log-classify only.

Profile name is always explicit (first arg after the verb, or -p/--ops, or MOW_OPS).

Commands:

  mow ops list                         list profiles
  mow ops show NAME                    summary (services, model, acp)
  mow ops check NAME                   validate profile
  mow ops services NAME                list services
  mow ops status NAME [service]        run actions.status
  mow ops incidents NAME               list incidents
  mow ops run NAME [flags]             remediation loop (or --once)

Run flags:

  --every 5m      interval (default: profile every, else 5m)
  --cron "…"      cron instead of --every
  --once          single scan then exit (for systemd oneshot / host cron)
  --prompt TEXT   override scan prompt (default: profile prompt or built-in)
  --allow-shell   required for ops_action (default: on for ops run)
  --allow-write   optional
  [engine flags]  --model --base-url --workspace --config …

Examples:

  mow ops list
  mow ops show fleet
  mow ops status fleet
  mow ops run fleet --every 5m
  mow ops run fleet --once
  MOW_OPS=fleet mow ops run fleet   # same; name still required on run

Tools (agent): ops_services, ops_logs, ops_action, ops_incident;
  plus acp_delegate when the profile declares acp.agents / service.acp.
`)
}

// takeName pulls the profile name from the first non-flag arg, else -p/MOW_OPS.
func takeName(opsFlag string, args []string) (name string, rest []string, err error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:], nil
	}
	name, err = resolveOpsName(opsFlag)
	return name, args, err
}

func peelOpsFlag(args []string) (opsName string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--ops":
			if i+1 < len(args) {
				opsName = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--ops="):
			opsName = strings.TrimPrefix(a, "--ops=")
		case strings.HasPrefix(a, "-p="):
			opsName = strings.TrimPrefix(a, "-p=")
		default:
			rest = append(rest, a)
		}
	}
	return opsName, rest
}

func cmdListProfiles(args []string) int {
	_ = args
	pack := loadPackConfig(nil)
	names, err := listProfiles(pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops list: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "ops root: %s\n", pack.root())
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "(no profiles)")
		fmt.Fprintf(os.Stderr, "create:  mkdir -p %s/<name> && edit config.yaml\n", pack.root())
		return 0
	}
	for _, n := range names {
		// One-line hint if profile loads
		p, err := loadProfile(n, pack)
		if err != nil {
			fmt.Printf("%s\t(error: %v)\n", n, err)
			continue
		}
		fmt.Printf("%-16s  %d service(s)", n, len(p.Services))
		if p.Model != "" {
			fmt.Printf("  model=%s", p.Model)
		}
		if e := strings.TrimSpace(p.Every); e != "" {
			fmt.Printf("  every=%s", e)
		}
		fmt.Println()
	}
	fmt.Fprintf(os.Stderr, "run:  mow ops run <name>\n")
	return 0
}

func cmdShow(opsFlag string, args []string) int {
	name, _, err := takeName(opsFlag, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops show: %v\n", err)
		return 2
	}
	pack := loadPackConfig(nil)
	p, err := loadProfile(name, pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops show: %v\n", err)
		return 1
	}
	fmt.Printf("profile:  %s\n", p.Name)
	fmt.Printf("dir:      %s\n", p.Dir)
	if p.Model != "" {
		fmt.Printf("model:    %s\n", p.Model)
	}
	if p.Wire != "" {
		fmt.Printf("wire:     %s\n", p.Wire)
	}
	if p.BaseURL != "" {
		fmt.Printf("base_url: %s\n", p.BaseURL)
	}
	if p.Workspace != "" {
		fmt.Printf("workspace:%s\n", p.Workspace)
	}
	if e := strings.TrimSpace(p.Every); e != "" {
		fmt.Printf("every:    %s\n", e)
	}
	if rp := strings.TrimSpace(p.RunPrompt); rp != "" {
		fmt.Printf("prompt:   %s\n", truncateLog(rp, 80))
	}
	if p.Prompt != "" {
		fmt.Printf("prompt.md: yes (%d bytes)\n", len(p.Prompt))
	}
	fmt.Printf("services: %d\n", len(p.Services))
	for _, s := range p.Services {
		fmt.Printf("  - %s", s.Name)
		if len(lookupAction(s.Actions, "restart")) > 0 {
			fmt.Printf("  [restart]")
		}
		if len(lookupAction(s.Actions, "status")) > 0 {
			fmt.Printf("  [status]")
		}
		if s.ACP != "" {
			fmt.Printf("  acp=%s", s.ACP)
		}
		if len(s.Logs) > 0 {
			fmt.Printf("  logs=%d", len(s.Logs))
		}
		fmt.Println()
	}
	if len(p.ACP.Agents) > 0 {
		fmt.Printf("acp peers: %d\n", len(p.ACP.Agents))
		for _, a := range p.ACP.Agents {
			fmt.Printf("  - %s  cmd=%v\n", a.Name, a.Command)
		}
	}
	fmt.Fprintf(os.Stderr, "run:  mow ops run %s\n", name)
	return 0
}

func cmdCheck(opsFlag string, args []string) int {
	name, _, err := takeName(opsFlag, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops check: %v\n", err)
		return 2
	}
	pack := loadPackConfig(nil)
	p, err := loadProfile(name, pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops check: %v\n", err)
		return 1
	}
	bad := 0
	if len(p.Services) == 0 {
		fmt.Fprintln(os.Stderr, "warn: no services")
		bad++
	}
	for _, s := range p.Services {
		if len(s.Logs) == 0 {
			fmt.Fprintf(os.Stderr, "warn: service %q has no logs\n", s.Name)
		}
		if len(lookupAction(s.Actions, "status")) == 0 && len(lookupAction(s.Actions, "restart")) == 0 {
			fmt.Fprintf(os.Stderr, "warn: service %q has no actions\n", s.Name)
		}
		for _, path := range s.Logs {
			if _, err := os.Stat(path); err != nil {
				fmt.Fprintf(os.Stderr, "warn: service %q log %s: %v\n", s.Name, path, err)
			}
		}
		if s.ACP != "" {
			found := false
			for _, a := range p.ACP.Agents {
				if strings.EqualFold(a.Name, s.ACP) {
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "warn: service %q acp %q not in acp.agents\n", s.Name, s.ACP)
				bad++
			}
		}
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "mow ops check %s: %d issue(s)\n", name, bad)
		return 1
	}
	fmt.Printf("ok %s (%d services)\n", name, len(p.Services))
	return 0
}

func cmdServices(opsFlag string, args []string) int {
	name, rest, err := takeName(opsFlag, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops services: %v\n", err)
		return 2
	}
	_ = rest
	pack := loadPackConfig(nil)
	p, err := loadProfile(name, pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops services: %v\n", err)
		return 1
	}
	if len(p.Services) == 0 {
		fmt.Fprintln(os.Stderr, "(no services)")
		return 0
	}
	for _, s := range p.Services {
		fmt.Printf("%s", s.Name)
		if s.ACP != "" {
			fmt.Printf("  acp=%s", s.ACP)
		}
		if len(lookupAction(s.Actions, "restart")) > 0 {
			fmt.Printf("  restart")
		}
		if len(lookupAction(s.Actions, "status")) > 0 {
			fmt.Printf("  status")
		}
		if len(s.Logs) > 0 {
			fmt.Printf("  logs=%d", len(s.Logs))
		}
		fmt.Println()
	}
	return 0
}

func cmdIncidents(opsFlag string, args []string) int {
	name, _, err := takeName(opsFlag, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops incidents: %v\n", err)
		return 2
	}
	pack := loadPackConfig(nil)
	p, err := loadProfile(name, pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops incidents: %v\n", err)
		return 1
	}
	out, err := listIncidents(p.incidentsDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops incidents: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}

func cmdStatus(opsFlag string, args []string) int {
	name, rest, err := takeName(opsFlag, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops status: %v\n", err)
		return 2
	}
	var service string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		service = rest[0]
	}
	pack := loadPackConfig(nil)
	p, err := loadProfile(name, pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops status: %v\n", err)
		return 1
	}
	runStatus := func(svcName string) int {
		argv, err := p.actionArgv(svcName, "status")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", svcName, err)
			return 1
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		out, err := cmd.CombinedOutput()
		fmt.Printf("%s %s\n", svcName, strings.TrimSpace(string(out)))
		if err != nil {
			return 1
		}
		return 0
	}
	if service != "" {
		return runStatus(service)
	}
	if len(p.Services) == 0 {
		fmt.Fprintln(os.Stderr, "(no services)")
		return 0
	}
	code := 0
	for _, s := range p.Services {
		if len(lookupAction(s.Actions, "status")) == 0 {
			fmt.Printf("%s (no actions.status)\n", s.Name)
			continue
		}
		if runStatus(s.Name) != 0 {
			code = 1
		}
	}
	return code
}

func cmdRun(opsFlag string, args []string) int {
	name, rest, err := takeName(opsFlag, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops run: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: mow ops run NAME [--every 5m|--once] [engine flags]")
		return 2
	}
	pack := loadPackConfig(nil)
	p, err := loadProfile(name, pack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops run: %v\n", err)
		return 1
	}

	fs := cliutil.NewFlagSet("ops run")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	every := fs.String("every", "", "interval (default: profile every, else 5m)")
	cronExpr := fs.String("cron", "", "5-field cron instead of --every")
	once := fs.Bool("once", false, "single scan then exit")
	promptFlag := fs.String("prompt", "", "scan prompt (default: profile prompt or built-in)")
	// ops_action needs shell; default on for ops run (opt-out with no way — always on)
	// Users can still pass --allow-shell explicitly; we force it for run.
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	ef.AllowShell = true // ops_action requires it

	// Profile LLM + workspace defaults when flags empty.
	if strings.TrimSpace(ef.Model) == "" && strings.TrimSpace(p.Model) != "" {
		ef.Model = p.Model
	}
	if strings.TrimSpace(ef.BaseURL) == "" && strings.TrimSpace(p.BaseURL) != "" {
		ef.BaseURL = p.BaseURL
	}
	if strings.TrimSpace(ef.Workspace) == "" && strings.TrimSpace(p.Workspace) != "" {
		ef.Workspace = p.Workspace
	}

	// Ensure child engines see this profile.
	_ = os.Setenv("MOW_OPS", name)
	applyProfileLLMEnv(p)

	prompt := strings.TrimSpace(*promptFlag)
	if prompt == "" {
		prompt = strings.TrimSpace(p.RunPrompt)
	}
	if prompt == "" {
		prompt = defaultOpsRunPrompt(name)
	}

	if *once {
		return runOnce(ef, name, prompt)
	}

	everyStr := strings.TrimSpace(*every)
	if everyStr == "" {
		everyStr = strings.TrimSpace(p.Every)
	}
	if everyStr == "" && strings.TrimSpace(*cronExpr) == "" {
		everyStr = "5m"
	}
	j, err := job.InlineJob("ops-"+name, everyStr, *cronExpr, "", prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops run: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := &job.Daemon{
		Schedules: []job.Job{j},
		NewEngine: func() (*mow.Engine, error) {
			_ = os.Setenv("MOW_OPS", name)
			applyProfileLLMEnv(p)
			return ef.NewEngineCLI()
		},
	}
	when := everyStr
	if c := strings.TrimSpace(*cronExpr); c != "" {
		when = "cron " + c
	}
	fmt.Fprintf(os.Stderr, "ops run %s  interval=%s  model=%s  ctrl+c to stop\n",
		name, when, firstNonEmpty(ef.Model, p.Model, os.Getenv("MOW_MODEL"), "(config)"))
	if err := d.Start(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mow ops run: %v\n", err)
		return 1
	}
	return 0
}

func runOnce(ef cliutil.EngineFlags, name, prompt string) int {
	fmt.Fprintf(os.Stderr, "ops run %s --once\n", name)
	eng, err := ef.NewEngineCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops run: %v\n", err)
		return 1
	}
	defer eng.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := eng.Prompt(ctx, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow ops run: %v\n", err)
		return 1
	}
	if t := strings.TrimSpace(res.Text); t != "" {
		fmt.Println(t)
	}
	return 0
}

func defaultOpsRunPrompt(name string) string {
	// Each ops run tick is an autonomous SRE turn: detect → ticket → remediate
	// (restart and/or acp_delegate peers) → close. Not a log classifier.
	return fmt.Sprintf(
		"You are the continuous fleet ops agent for profile %q (ops=%s / MOW_OPS). "+
			"Mission: keep services healthy — monitor logs and status, open incidents only for issues that need attention, "+
			"then fix them (ops_action restart when stuck; acp_delegate to the service's peer for code/config fixes). "+
			"Workflow each tick: (1) ops_incident list (2) ops_action status per service (3) ops_logs with greps for ERROR/WARN/panic/5xx/timeout "+
			"(4) for each real problem: open/update incident with a stable signature, take a remediation step, note it on the incident "+
			"(5) close or mark mitigated when fixed or clearly stale. "+
			"Do not open tickets for one-off historical lines with no recurrence and healthy status. "+
			"Do not thrash restarts. Prefer file logs. End with findings, actions taken (including peer work), and open incidents.",
		name, name,
	)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// truncateLog clamps s to n bytes, cutting on a rune boundary. Callers pass
// service log text and prompts, which are routinely non-ASCII.
func truncateLog(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
