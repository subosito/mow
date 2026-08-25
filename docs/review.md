# mow review / mow sec

Ships on **`mow-full`** (`cmd/mow-full`), not the lean `mow` binary. Invoke as
`mow-full review` / `mow-full sec` (examples below keep the short `mow review`
form for the subcommand itself).

Two AI-assisted review commands over one read-only workflow. `mow review` looks
for correctness and maintainability problems; `mow sec` reads the same code
adversarially for security problems. They share a schema, a scope resolver, a
two-pass verification workflow, and a set of renderers. Specialization is
internal (persona, taxonomy, defaults) — the public surface is the two
commands, not a profile flag. Security-specific evidence, safety boundaries,
and future stages are documented in [sec.md](sec.md).

Both are **advisory**. They are not a scanner, not a pentest, and not proof
that the code is correct or secure. Static analyzers (Semgrep, CodeQL, gosec,
Trivy) remain complementary: they are sound where they apply, this is a reader
that understands intent.

```bash
mow review                                  # review uncommitted work
mow review --diff main...HEAD               # review a branch
mow review ./internal/api                   # review a package
mow sec --staged --fail-on high             # pre-commit security gate
mow sec --format sarif --output sec.sarif   # for code scanning
```

## Why two passes

A single prompt that asks a model for findings produces confident noise. The
workflow splits discovery from judgment:

| Pass | Input | Allowed output |
|------|-------|----------------|
| 1 — discovery | scope briefing (diffs + line-numbered content) | candidate findings as JSON |
| 2 — verification | candidate digests + bounded line-numbered excerpts around each cited location; tools for wider context, **not** pass 1's JSON | confirm / reject / correct severity+confidence; on `mow sec`, optionally correct or clear structured evidence fields |

Both passes run `ReadOnly` **and** `Ephemeral`. Ephemeral matters: pass 1's JSON
never becomes pass 2's context, so the verifier has to re-derive the evidence
from the code instead of agreeing with its own earlier reasoning. The verifier
may only rule on ids that already exist — it cannot introduce a new finding. On
`mow sec`, it may correct or clear pass-one structured evidence fields when it
returns them explicitly in `evidence_fields`.

Use `--verifier` to run pass two with a different read-only model than pass
one (default: same engine, or the first `--reviewer` when using an ensemble).
`--reviewers` is an alias of `--reviewer`. Slash `/review` and `/sec` use the
session model only and do not start an ensemble.

Rules that follow from this:

- A candidate with **no verdict** is marked unverified, never an implicit pass.
- Duplicate, unknown, or empty verdict ids are contract errors.
- Pass 1 cannot set verification provenance: `verified` is cleared before the
  workflow and only pass 2 can set it true.
- Unverified findings are **suppressed by default** (`--include-unverified` keeps them).
- A reply that is not valid JSON, omits/nulls the required `findings` or
  `verdicts` arrays, or violates the contract is a **hard error** — "looks fine
  to me" can never render as a clean review.
- An empty scope is a **successful empty review** that says nothing was reviewed,
  and warns on stderr even under `--quiet`.

## Scope

Selectors are mutually exclusive and resolved in a fixed order:

`--diff` → `--staged` → `--base` → `[paths...]` → dirty worktree → whole tree.

Bare `mow review` on a dirty repo reviews **uncommitted work** — cheap, and it
matches intent. Because the default varies with worktree state, the scope header
always discloses what was actually selected.

Every skipped file carries a **reason** (excluded glob, vendored, generated,
binary, too large, budget exhausted); `--verbose` prints them. The skip list is
capped at 256 entries. `--budget small|medium|large` caps files, bytes, per-file
bytes, and turns. Path walks SkipDir default-exclude trees (`node_modules`,
`vendor`, …) unless `--include-all`. Walks stop after 4096 remaining files so a
vendored tree cannot hide source. A truncated scope is flagged in every format
so a partial review cannot look like a complete clean scan.

## Command defaults

| Command | Default report floor | Default failing severity |
|---------|----------------------|--------------------------|
| `mow review` | low | high |
| `mow sec` | medium | high |

## Output

| Format | Use |
|--------|-----|
| `text` (default) | humans; worst-first blocks, opt-in ANSI color |
| `json` | the source of truth; one flat object per finding |
| `jsonl` | line 1 envelope, then one finding per line (`jq`, streaming) |
| `sarif` | SARIF 2.1.0 for GitHub/GitLab code scanning |

Findings are stable and machine-friendly:

```json
{
  "schema_version": 1,
  "profile": "security",
  "advisory": true,
  "run":   { "tool": "mow sec", "model": "…", "commit": "…", "branch": "…" },
  "scope": { "mode": "diff", "selection": "main...HEAD", "files_reviewed": 12 },
  "counts":{ "critical": 0, "high": 1, "medium": 2, "low": 0, "info": 0, "total": 3 },
  "findings": [
    {
      "id": "sec-001",
      "fingerprint": "sha256:…",
      "severity": "high",
      "confidence": "high",
      "category": "authz",
      "path": "internal/api/users.go",
      "start_line": 87,
      "end_line": 90,
      "evidence": "…",
      "impact": "…",
      "recommendation": "…",
      "source": "HTTP path parameter id",
      "sink": "UPDATE users SET … WHERE id=$1 without ownership check",
      "sanitizers_considered": "none found on handler path",
      "reachability": "reachable for any authenticated session",
      "attacker_prerequisites": "valid session cookie",
      "evidence_limitations": "middleware authz not fully inspected",
      "attack_surface": "authenticated HTTP API",
      "trust_boundary": "user → tenant data"
    }
  ]
}
```

Notes on the schema:

- **`scope.mode`** tells a consumer how the scope was chosen; `scope.diff` is
  present *only* for `diff`/`base`, so a path-scoped report never advertises a
  git range that does not exist.
- **Fingerprints** are content-based and line-drift immune, so a finding can be
  tracked across runs (exported as SARIF `partialFingerprints`).
- **Profile extras** are flattened into the finding object, not nested under
  `extra`. Security may emit optional evidence fields (`source`, `sink`,
  `sanitizers_considered`, `reachability`, `attacker_prerequisites`,
  `evidence_limitations`, `attack_surface`, `trust_boundary`, `exploitability`,
  `cwe`); general review may emit `affected_behavior` / `test_gap`. Consumers
  must treat all extras as optional for backward compatibility.
- **`verified`** is set only by pass 2. High confidence from pass 1 is not
  “model-verified” until the verifier confirms the claim from code. On `mow sec`,
  `evidence_level: model-verified` additionally requires `verified: true` — a
  complete-looking but unconfirmed finding is capped at `code-supported`.
- **SARIF rule ids are profile-namespaced** (`mow/security/authz`) so review and
  sec findings do not collide in one dashboard.
- Secrets are **redacted** before anything is rendered or written.


## Validation

The model does not get to define reality. Before rendering, every candidate is
validated against the resolved scope:

- paths normalized; absolute, backslash, `":line"`, and traversal forms rejected
- files outside the scope dropped
- line numbers clamped to the file's real length (a hallucinated line 900 in a
  120-line file cannot point reviewers at nothing)
- duplicates merged on fingerprint
- deterministic worst-first ordering, then sequential `review-NNN` / `sec-NNN` ids

Every drop is recorded with a reason and shown under `--verbose`.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | no findings at or above `--fail-on` (and not failing on truncation) |
| 1 | findings at or above `--fail-on` (default: profile's, `high`), or truncated scope with `--fail-on-truncated` |
| 2 | error — bad selector, unreachable model, contract violation |

`--exit-zero` forces 0 on a *successful* run for advisory CI. It does not apply
to errors: a run with no report exits 2, because "no report" must never read as
passing. It also overrides `--fail-on` findings and `--fail-on-truncated` (same
advisory contract). `--fail-on-truncated` exits 1 when the scope was truncated
(partial coverage) even if no findings meet `--fail-on`; default off so
truncation is disclosed but not blocking unless you opt in.

Finished reports include an `exit` object when the run would exit non-zero under
the active exit policy, with machine-readable `reasons` (`truncated_scope`,
`finding_severity`) so CI can distinguish truncation from severity failures
without changing the exit-code contract (both remain code `1`). Clean runs omit
`exit` (zero value). Library `Run` stamps the same field from `Request.ExitPolicy`
(CLI flags map into that policy). Text and SARIF output surface the same reasons.

```yaml
# advisory security job
- run: mow sec --diff origin/main...HEAD --format sarif --output sec.sarif --exit-zero
# blocking gate
- run: mow sec --diff origin/main...HEAD --fail-on high
# strict coverage gate (fail when budget truncated the scope)
- run: mow sec --diff origin/main...HEAD --fail-on high --fail-on-truncated
```

## Safety

`AllowWrite` and `AllowShell` are forced **off** regardless of config or flags,
so a review can never modify the code it is reviewing. Sessions are disabled —
the report is the artifact, not the conversation.

Scope gathering reads only **regular files under the workspace root**. The
workspace directory itself may be a symlink (the intended tree). Symlink paths
*under* the workspace are rejected or skipped so a planted link cannot redirect
reads outside it. Absolute paths and `..` forms are rejected during finding
validation as before.

Model output is bounded before it reaches the report: raw replies above 4 MiB,
more than 200 candidate findings, or more than 128 workflow notes fail the run
with an actionable error rather than producing a truncated or misleading report.
Free-form finding fields are clamped to 4000 characters each.

Each pass runs with `ReadOnly` + `Ephemeral` prompts and a strict per-call
`AllowedTools` allowlist of `read`, `glob`, and `grep` only. Candidate and
verifier engines are built with `Options.SkipExtensionSetup` and
`Options.DisableExtensionHooks`, so constructing them does not run extension
`BeforeNew` setup (MCP/cmdhook processes) or inherit extension lifecycle
hooks. User LLM config still loads; `extensions.review` budgets are read before
scope resolve via the pack's own config loader, not via engine construction.
The report records
`run.read_only: true` and `run.tool_policy: builtin_read_inspect_only`. MCP, ACP,
and other extension tools are omitted from tool specs and denied at execution even
when they declare `ReadOnly() true`. Write, edit, bash, `recall`, and
`understand_*` are excluded. Extension authors must not rely on review exposing
their tools.

## Design notes

- **Two commands, one implementation.** Internally a *profile* selects persona,
  taxonomy, severity floors, and extras; users only run `mow review` or
  `mow sec`. Report JSON still includes `"profile"` for machine provenance
  (SARIF rule ids are namespaced the same way).
- **Validation over prompting.** Anything that can be checked mechanically —
  paths, lines, scope, duplicates, ordering, ids — is checked in code, so the
  prompt only has to carry judgment.
- **The report states what it does not mean.** An empty report explicitly says
  it is not proof the code is correct/secure, and truncation is disclosed in
  text, JSON, and SARIF.

## Dogfooding

`mow review` was run against its own command layer. It found three real bugs,
all confirmed by the verification pass, all since fixed — a useful sample of
what the tool is good at (contract and edge-case reasoning, not style):

| Finding | Why it mattered |
|---------|-----------------|
| Help pre-scan ignored the `--` terminator and matched flag *values* | `mow review -- --help` printed usage and exited 0 — in CI, indistinguishable from a clean review |
| `--output` rendered directly over the target (`os.Create` truncates) | a failed render destroyed the previous good report and published a truncated artifact; now written to a temp file and renamed atomically |
| `parseArgs` error returned a bare exit code, bypassing `fail()` | inconsistent diagnostics, and the stdlib `-help` spelling exited 2 instead of 0 |

Each is now pinned by a regression test. The pattern worth noting: all three
were **exit-code and artifact-integrity** bugs — exactly the failures that make
a review tool lie about its own result, and exactly what a human skims past.

### Cost and latency

A two-pass review is not cheap. On a capable model, expect **minutes** for a
handful of files, because both passes read the cited code with `read`/`grep`
before ruling. Practical guidance:

- Scope tightly — `--diff`/`--staged` on a change, not the whole tree.
- `--budget small` for a quick pass; `large` only when the change is genuinely
  broad.
- `--no-verify` roughly halves the work and is fine for a local skim, but the
  report is explicitly marked unverified and will be noisier.
- In CI, prefer `mow sec --diff origin/main...HEAD` on pull requests over a
  full-tree scan on every push.

Progress is printed on stderr as tool lines so a long run is visibly working
rather than hung; `--quiet` suppresses it for scripted use.

Each budget also caps agent turns (30 / 45 / 70). The cap has two failure modes
and sits between them: uncapped, a capable model will spend twenty-plus turns
re-reading files it already has in the scope briefing; capped too tightly, the
pass spends its whole budget exploring and never emits a report. If a pass does
exhaust its turns, the run fails with exit `2` and says so, rather than
reporting a partial result — or an empty one — as a finished review.

### Tuning budgets (`extensions.review`)

The built-in sizes cannot fit every repository — a tree larger than the large
budget's 120-file cap truncates no matter which size you pick. Budget caps are
therefore configurable under `extensions.review` in `-config` or
`$MOW_HOME/config.yaml`:

```yaml
extensions:
  review:
    budgets:
      large:
        max_files: 200        # default 120
        max_bytes: 2000000    # default 1_200_000
        max_file_bytes: 200000
        max_turns: 90
```

Only the keys you set change; the rest keep their built-in values, so raising
`max_files` alone does not quietly remove the byte or turn caps. An unknown
budget name or a non-positive cap is a hard error rather than a silent
fallback: a user who raised a cap and got the built-in one instead would
believe more had been reviewed than was.

Nothing else is configurable. Personas, taxonomies, severity floors, and the
two-pass workflow are the product — if a project could weaken them, a reader
could no longer tell what `mow sec` actually did from the fact that it ran.
Because the section is read from `$MOW_HOME` and explicit `-config` paths only,
project config cannot reach it.

Raising a cap is usually the wrong first move. A single review of a very large
scope is a weaker review than several narrow ones: attention spreads thin, and
the verification pass has more to hold. Prefer `--diff`, a path, or `--exclude`
first, and raise the budget when the scope is genuinely irreducible.

## Group review

Pass one can use several independently configured models. Repeat `--reviewer`
or pass a comma-separated list; `--reviewers` is an alias. The listed order is
retained and the first reviewer runs the existing verification pass:

```bash
mow review --reviewer gpt-5-mini --reviewer claude-sonnet-4 --reviewer-parallel 2
mow sec --reviewer gpt-5-mini,claude-sonnet-4 --diff main...HEAD
mow sec --reviewer gpt-5-mini,claude-sonnet-4 --verifier claude-sonnet-4
```

Each selected model receives its own read-only, ephemeral engine. Candidate JSON
is parsed independently, merged, and passed through the ordinary validation and
verification workflow. A member failure, malformed JSON, or cancellation fails
the review rather than producing a partial clean report. `--reviewer-parallel`
bounds concurrent candidate calls; its default runs every listed model at once.
Findings include a backward-compatible `reviewer` extra field identifying the
candidate model when an ensemble is used. Provenance extras:

| Field | Meaning |
|---|---|
| `reviewer_count` | How many candidate reviewers reported this fingerprint |
| `reviewer_consensus` | `single` (one reviewer) or `independent` (2+ reviewers merged on fingerprint) |
| `reviewers` | Comma-separated list when `reviewer_consensus` is `independent` |
| `verifier_agreement` | Pass-two outcome on reported findings: `confirmed`, `confirmed_independent`, `uncertain`, or `uncertain_independent` |

When several reviewers report the same fingerprint, a `reviewers` field lists every
model that surfaced it. Without reviewer flags, review uses its existing single
engine. By default the first listed reviewer also runs pass two; `--verifier` overrides that with a dedicated verifier. Pass two is always one model — a list is rejected.

Programmatic callers may likewise use `NewEnsembleReviewer` with named,
read-only `Reviewer` values, or pass `Request.Verifier` for a dedicated pass-two
model. `ValidateRequest` rejects `Verifier` together with `SkipVerification`.
ACP peers are not used: read-only review denies `acp_delegate` and this policy
remains unchanged.


### Does it depend on the model?

Yes, but less than you would expect, and the failure mode is safe. Against a
fixture with five planted flaws (SQL injection, command injection, path
traversal, missing authorization, hardcoded credential), the security profile
found all five, with correct CWE ids and line numbers, and the verification
pass confirmed each one while reasoning explicitly about reachability.

Refusal was not observed: framing the task as defensive review of code the user
controls, with a strict output contract, keeps models on task. What does vary
is *thoroughness* and *turn economy* — weaker or faster models explore less and
may miss subtler issues, and a model that never emits contract-shaped JSON
fails the run instead of returning a clean report. That asymmetry is deliberate:
a review can under-report, but it must never claim a clean result it did not
earn.

Model choice is a plain flag — `mow sec --model <id>` — and scope defaults to
the working tree, so `mow sec` alone reviews uncommitted work, `mow sec PATH…`
reviews paths, and `mow sec --diff HEAD~1...HEAD` reviews a commit. Reviewing
this repository with three different models produced usable reports from each.

### Keep the scope small

The practical limit is scope size, not model quality. A 40-file scope asked a
capable model to emit one JSON object covering everything, and it returned
prose instead — the run failed with exit `2` rather than reporting partial
results, which is the safe outcome but still a failed run. Two to fifteen files
is the range where reports come back reliably.

Prefer `--diff`/`--staged` on a change over a whole-tree sweep, and reach for
`--budget large` only when the change really is broad. If a review fails to
produce a report, narrowing the scope is usually the fix.

## See also

- [mow sec](sec.md) — security-specific evidence and read-only boundaries
- [Documentation index](README.md)
