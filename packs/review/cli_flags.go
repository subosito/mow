package review

import (
	"flag"
	"fmt"
	"strings"
)

// CLIFlags are the user-facing flags shared by `mow review` and `mow sec`.
// Kept in the pack (not cliutil) because they are review-specific.
//
// There is deliberately no profile field: the persona is chosen by the command
// entry point and passed to Resolve, so a flag struct can never disagree with
// the command the user actually typed.
type CLIFlags struct {
	Diff             string
	Staged           bool
	Base             string
	Format           string
	Output           string
	MinSeverity      string
	FailOn           string
	Budget           string
	Excludes         []string
	IncludeAll       bool
	IncludeLow       bool
	Unverified       bool
	NoVerify         bool
	ExitZero         bool
	NoColor          bool
	Quiet            bool
	Reviewers          []string
	ReviewerParallel   int
	VerifierModel      string
	verifierModelAlias string
	FailOnTruncated    bool
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
	fs.Var((*repeatable)(&f.Reviewers), "reviewer", "candidate reviewer model (repeatable or comma-separated)")
	fs.Var((*repeatable)(&f.Reviewers), "reviewers", "alias of --reviewer")
	fs.IntVar(&f.ReviewerParallel, "reviewer-parallel", 0, "maximum concurrent candidate reviewers (0=all)")
	fs.StringVar(&f.VerifierModel, "verifier", "", "pass-two verifier model (one model; default: first reviewer)")
	fs.StringVar(&f.verifierModelAlias, "verifier-model", "", "alias of --verifier")
	fs.BoolVar(&f.FailOnTruncated, "fail-on-truncated", false, "exit non-zero when scope was truncated")
}

// normalizeVerifier accepts --verifier (preferred) or --verifier-model (alias).
// Pass two is a single judge: a comma list is rejected rather than becoming
// a second ensemble.
func (f *CLIFlags) normalizeVerifier() error {
	primary := strings.TrimSpace(f.VerifierModel)
	alias := strings.TrimSpace(f.verifierModelAlias)
	switch {
	case primary != "" && alias != "" && !strings.EqualFold(primary, alias):
		return fmt.Errorf("--verifier and --verifier-model disagree (%q vs %q)", primary, alias)
	case primary == "":
		primary = alias
	}
	if strings.Contains(primary, ",") {
		return fmt.Errorf("--verifier takes one model (got %q); pass two is a single judge", primary)
	}
	f.VerifierModel = primary
	return nil
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
//
// prof is the internal persona selected by the command entry point; a nil
// profile defaults to general.
func (f *CLIFlags) Resolve(prof *Profile, workspace string, paths []string) (Request, Format, ExitPolicy, error) {
	var req Request
	if prof == nil {
		prof = GeneralProfile()
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

	policy := ExitPolicy{ExitZero: f.ExitZero, FailOnTruncated: f.FailOnTruncated}
	if s := strings.TrimSpace(f.FailOn); s != "" {
		v, ok := ParseSeverity(s)
		if !ok {
			return req, "", ExitPolicy{}, fmt.Errorf("unknown --fail-on %q (want %s)",
				s, strings.Join(SeverityNames(), ", "))
		}
		policy.FailOn = v
	}

	if err := f.normalizeVerifier(); err != nil {
		return req, "", ExitPolicy{}, err
	}
	if f.NoVerify && strings.TrimSpace(f.VerifierModel) != "" {
		return req, "", ExitPolicy{}, fmt.Errorf("--verifier cannot be used with --no-verify")
	}

	req = Request{
		Profile:           prof,
		MinSeverity:       minSev,
		IncludeUnverified: f.Unverified,
		SkipVerification:  f.NoVerify,
		ExitPolicy:        policy,
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

