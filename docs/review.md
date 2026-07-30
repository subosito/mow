# mow review / mow sec

Two AI-assisted review commands over one read-only workflow. `mow review` looks
for correctness and maintainability problems; `mow sec` reads the same code
adversarially for security problems. They share a schema, a scope resolver, a
two-pass verification workflow, and a set of renderers — only the **profile**
differs.

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
| 2 — verification | rendered candidate digests + the code, **not** pass 1's JSON | confirm / reject / correct severity+confidence |

Both passes run `ReadOnly` **and** `Ephemeral`. Ephemeral matters: pass 1's JSON
never becomes pass 2's context, so the verifier has to re-derive the evidence
from the code instead of agreeing with its own earlier reasoning. The verifier
may only rule on ids that already exist — it cannot introduce a claim or rewrite
the evidence.

Rules that follow from this:

- A candidate with **no verdict** is marked unverified, never an implicit pass.
- Unverified findings are **suppressed by default** (`--include-unverified` keeps them).
- A reply that is not valid JSON, or violates the contract, is a **hard error** —
  "looks fine to me" can never render as a clean review.
- An empty scope is a **successful empty review** that says nothing was reviewed,
  and warns on stderr even under `--quiet`.

## Scope

Selectors are mutually exclusive and resolved in a fixed order:

`--diff` → `--staged` → `--base` → `[paths...]` → dirty worktree → whole tree.

Bare `mow review` on a dirty repo reviews **uncommitted work** — cheap, and it
matches intent. Because the default varies with worktree state, the scope header
always discloses what was actually selected.

Every skipped file carries a **reason** (excluded glob, vendored, generated,
binary, too large, budget exhausted); `--verbose` prints them. `--budget
small|medium|large` caps files, bytes, per-file bytes, and turns; a truncated
scope is flagged in every format so a partial review cannot look like a complete
clean scan.

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
      "attack_vector": "network"
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
- **Profile extras** (`attack_vector`, `asset_at_risk`, …) are flattened into
  the finding object, not nested under `extra`.
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
| 0 | no findings at or above `--fail-on` |
| 1 | findings at or above `--fail-on` (default: profile's, `high`) |
| 2 | error — bad selector, unreachable model, contract violation |

`--exit-zero` forces 0 on a *successful* run for advisory CI. It does not apply
to errors: a run with no report exits 2, because "no report" must never read as
passing.

```yaml
# advisory security job
- run: mow sec --diff origin/main...HEAD --format sarif --output sec.sarif --exit-zero
# blocking gate
- run: mow sec --diff origin/main...HEAD --fail-on high
```

## Safety

`AllowWrite` and `AllowShell` are forced **off** regardless of config or flags,
so a review can never modify the code it is reviewing. Sessions are disabled —
the report is the artifact, not the conversation.

## Design notes

- **Profiles, not two implementations.** `mow sec` is `--profile security` with
  a stricter default floor (medium vs low), the security taxonomy, extra
  finding fields, and adversarial verification questions (reachability,
  attacker control, upstream validation, framework protection).
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

Each budget also caps agent turns (12 / 20 / 36). This is deliberate: left
uncapped, a capable model will spend twenty-plus turns re-reading files it
already has in the scope briefing, costing minutes without improving the
findings. If a pass exhausts its turns before emitting a report, the run fails
with exit `2` rather than reporting a partial result as a finished review.
