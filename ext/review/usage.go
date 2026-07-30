package review

import (
	"fmt"
	"os"
)

// printUsage writes the help for `mow review` or `mow sec`.
func printUsage(cmd string) {
	if cmd == "sec" {
		printSecUsage()
		return
	}
	printReviewUsage()
}

func printReviewUsage() {
	fmt.Fprint(os.Stderr, `mow review — AI-assisted code review (advisory)

Reviews a diff or explicit paths and reports evidence-backed findings about
correctness, error handling, tests, API compatibility, concurrency, and
maintainability. Findings are suggestions, not proof that the code is correct.

Usage:

  mow review [paths...] [flags]

Scope (mutually exclusive; default reviews uncommitted changes, else the tree):

  --diff main...HEAD       review a git range
  --staged                 review staged changes
  --base origin/main       review changes against a base ref
  [paths...]               review explicit files or directories

Filtering:

  --min-severity medium    lowest severity to report (default: low)
  --include-low            report low/info findings too
  --include-unverified     keep findings the verification pass could not confirm
  --exclude 'vendor/**'    skip a glob (repeatable)
  --include-all            do not skip vendor/generated/lockfiles
  --budget small|medium|large   how much code to pull in (default: medium)

Output:

  --format text|json|jsonl|sarif   (default: text)
  --output review.json     write to a file instead of stdout
  --no-color               disable ANSI color
  --quiet                  no progress on stderr

CI:

  --fail-on high           lowest severity that exits 1 (default: high)
  --exit-zero              always exit 0 on a successful run

  Exit codes: 0 clean · 1 findings at/above --fail-on · 2 error

Other:

  --profile general|security   security is equivalent to 'mow sec'
  --no-verify              skip the verification pass (faster, noisier)
  [engine flags]           --model --base-url --workspace --config …

Examples:

  mow review                                 review uncommitted work
  mow review --diff main...HEAD              review a branch
  mow review ./internal/api                  review a package
  mow review --format sarif --output r.sarif  for code scanning
  mow review --staged --fail-on medium       pre-commit gate

The review runs read-only: write and shell tools are disabled regardless of
config, so it can never modify the code it is reviewing.

Security review: mow sec
`)
}

func printSecUsage() {
	fmt.Fprint(os.Stderr, `mow sec — AI-assisted security review (advisory)

Reads a diff or paths adversarially and reports evidence-backed security
findings: authn/authz gaps, injection, SSRF, path traversal, secret leaks,
crypto misuse, and unsafe configuration. This is not a scanner, a pentest, or
proof that the code is secure.

Usage:

  mow sec [paths...] [flags]

Equivalent to 'mow review --profile security' with stricter defaults:
reports from medium severity up and applies the security taxonomy and playbook.

Scope (mutually exclusive; default reviews uncommitted changes, else the tree):

  --diff main...HEAD       review a git range
  --staged                 review staged changes
  --base origin/main       review changes against a base ref
  [paths...]               review explicit files or directories

Filtering:

  --min-severity high      lowest severity to report (default: medium)
  --include-low            report low/info findings too
  --include-unverified     keep findings the verification pass could not confirm
  --exclude 'vendor/**'    skip a glob (repeatable)
  --include-all            do not skip vendor/generated/lockfiles
  --budget small|medium|large   how much code to pull in (default: medium)

Output:

  --format text|json|jsonl|sarif   (default: text)
  --output findings.sarif  write to a file instead of stdout
  --no-color               disable ANSI color
  --quiet                  no progress on stderr

CI:

  --fail-on high           lowest severity that exits 1 (default: high)
  --exit-zero              always exit 0 on a successful run

  Exit codes: 0 clean · 1 findings at/above --fail-on · 2 error

Other:

  --no-verify              skip the verification pass (faster, noisier)
  [engine flags]           --model --base-url --workspace --config …

Examples:

  mow sec                                    review uncommitted work
  mow sec --diff main...HEAD                 review a branch
  mow sec ./internal/auth                    review a sensitive package
  mow sec --format sarif --output sec.sarif  for code scanning
  mow sec --fail-on critical --exit-zero     advisory CI job

The review runs read-only: write and shell tools are disabled regardless of
config. Static analyzers (Semgrep, CodeQL, gosec, Trivy) remain complementary.

General code review: mow review
`)
}
