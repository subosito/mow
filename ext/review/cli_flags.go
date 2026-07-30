package review

import (
	"flag"
	"fmt"
	"strings"
)

// CLIFlags are the user-facing flags shared by `mow review` and `mow sec`.
// Kept in the pack (not cliutil) because they are review-specific.
//
// Profile is set by the command entry (review → general, sec → security), not
// by a public flag. Library callers may set it before Resolve; unknown names
// still fail validation.
type CLIFlags struct {
	Diff        string
	Staged      bool
	Base        string
	Profile     string // internal: "general" | "security" (not a CLI flag)
	Format      string
	Output      string
	MinSeverity string
	FailOn      string
	Budget      string
	Excludes    []string
	IncludeAll  bool
	IncludeLow  bool
	Unverified  bool
	NoVerify    bool
	ExitZero    bool
	NoColor     bool
	Quiet       bool
}

// Bind registers the review flags on fs.
func (f *CLIFlags) Bind(fs *flag.FlagSet) {
	fs.StringVar(&f.Diff, "diff", "", "review a git range, e.g. main...HEAD")
	fs.BoolVar(&f.Staged, "staged", false, "review staged changes")
	fs.StringVar(&f.Base, "base", "", "review changes against a base ref, e.g. origin/main")
	fs.StringVar(&f.Format, "format", "text", "output format: "+strings.Join(FormatNames(), "|"))
	fs.StringVar(&f.Output, "output", "", "write the report to a file instead of stdout")
	fs.StringVar(&f.MinSeverity, "min-severity", "", "lowest severity to report ("+strings.Join(SeverityNames(), "|")+")")
	fs.StringVar(&f.FailOn, "fail-on", "", "lowest severity that exits non-zero")
	fs.StringVar(&f.Budget, "budget", "medium", "scope budget: "+strings.Join(BudgetNames(), "|"))
	fs.Var((*repeatable)(&f.Excludes), "exclude", "glob to skip (repeatable), e.g. 'vendor/**'")
	fs.BoolVar(&f.IncludeAll, "include-all", false, "do not skip vendor/generated/lockfiles")
	fs.BoolVar(&f.IncludeLow, "include-low", false, "report low/info findings too")
	fs.BoolVar(&f.Unverified, "include-unverified", false, "keep findings the verification pass could not confirm")
	fs.BoolVar(&f.NoVerify, "no-verify", false, "skip the verification pass (faster, noisier)")
	fs.BoolVar(&f.ExitZero, "exit-zero", false, "always exit 0 on a successful run (advisory CI)")
	fs.BoolVar(&f.NoColor, "no-color", false, "disable ANSI color in text output")
	fs.BoolVar(&f.Quiet, "quiet", false, "suppress progress output on stderr")
}

// repeatable is an append-on-Set string slice flag.
type repeatable []string

func (r *repeatable) String() string {
	if r == nil {
		return ""
	}
	return strings.Join(*r, ",")
}

func (r *repeatable) Set(v string) error {
	if v = strings.TrimSpace(v); v != "" {
		*r = append(*r, v)
	}
	return nil
}

// Resolve turns parsed flags plus positional paths into a Request, an output
// Format, and an ExitPolicy. All user-facing validation happens here so the
// command surface fails fast with a clear message instead of mid-review.
func (f *CLIFlags) Resolve(workspace string, paths []string) (Request, Format, ExitPolicy, error) {
	var req Request
	// Empty → general (mow review). Callers that pin sec set Profile first.
	name := strings.TrimSpace(f.Profile)
	if name == "" {
		name = "general"
	}
	prof, ok := LookupProfile(name)
	if !ok {
		return req, "", ExitPolicy{}, fmt.Errorf("unknown profile %q (internal: want %s)",
			name, strings.Join(ProfileNames(), ", "))
	}
	format, err := ParseFormat(f.Format)
	if err != nil {
		return req, "", ExitPolicy{}, err
	}
	if _, ok := LookupBudget(f.Budget); !ok {
		return req, "", ExitPolicy{}, fmt.Errorf("unknown budget %q (want %s)",
			f.Budget, strings.Join(BudgetNames(), ", "))
	}
	// Selectors are mutually exclusive: silently picking one would hide what
	// was actually reviewed.
	if n := boolCount(f.Diff != "", f.Staged, f.Base != ""); n > 1 {
		return req, "", ExitPolicy{}, fmt.Errorf("--diff, --staged, and --base are mutually exclusive")
	}
	if len(paths) > 0 && (f.Diff != "" || f.Staged || f.Base != "") {
		return req, "", ExitPolicy{}, fmt.Errorf("paths cannot be combined with --diff/--staged/--base")
	}

	minSev := prof.MinSeverity
	if s := strings.TrimSpace(f.MinSeverity); s != "" {
		v, ok := ParseSeverity(s)
		if !ok {
			return req, "", ExitPolicy{}, fmt.Errorf("unknown --min-severity %q (want %s)",
				s, strings.Join(SeverityNames(), ", "))
		}
		minSev = v
	} else if f.IncludeLow {
		minSev = SevInfo
	}

	policy := ExitPolicy{ExitZero: f.ExitZero}
	if s := strings.TrimSpace(f.FailOn); s != "" {
		v, ok := ParseSeverity(s)
		if !ok {
			return req, "", ExitPolicy{}, fmt.Errorf("unknown --fail-on %q (want %s)",
				s, strings.Join(SeverityNames(), ", "))
		}
		policy.FailOn = v
	}

	req = Request{
		Profile:           prof,
		MinSeverity:       minSev,
		IncludeUnverified: f.Unverified,
		SkipVerification:  f.NoVerify,
		Scope: ScopeRequest{
			Workspace:  workspace,
			Paths:      paths,
			Diff:       f.Diff,
			Staged:     f.Staged,
			Base:       f.Base,
			Excludes:   f.Excludes,
			IncludeAll: f.IncludeAll,
			Budget:     f.Budget,
		},
	}
	return req, format, policy, nil
}

func boolCount(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}
