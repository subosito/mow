# mow sec

`mow sec` is mow's read-only, evidence-backed security review command. It reads
a bounded code scope adversarially, discovers candidate vulnerabilities, asks an
independent ephemeral model pass to verify them, mechanically validates the
result, and emits an advisory report.

It is intentionally not a scanner, penetration test, exploit runner, or proof
that code is secure. It never writes the reviewed tree and never executes
project code. General review behavior shared with `mow review`—scope resolution,
budgets, formats, exit policy, and the base report schema—is documented in
[review.md](review.md).

## Quick start

```bash
mow sec                                  # uncommitted work, or whole tree when clean
mow sec --staged                         # staged changes
mow sec --diff main...HEAD               # branch/range
mow sec --base origin/main               # changes from a base
mow sec ./internal/auth ./internal/api   # selected paths
mow sec --budget large --fail-on medium  # larger scope and stricter CI gate
mow sec --format sarif --output sec.sarif
```

The default report floor is `medium`; the default failing severity is `high`.
Use `--include-low`, `--include-unverified`, `--min-severity`, `--fail-on`, and
`--exit-zero` to change reporting or CI policy. These switches affect output,
not what the reviewer is allowed to do.

## Security review workflow

### Pass 1: adversarial discovery

The discovery reviewer looks for evidence-backed security defects across trust
boundaries, including authentication and authorization gaps, injection, SSRF,
path traversal, unsafe deserialization, XSS, secret leakage, cryptographic
misuse, supply-chain risks, and denial of service.

For each candidate it is asked to establish:

```text
attacker-controlled source
→ transformations
→ guards or sanitizers considered
→ dangerous sink
```

It must also consider framework protections, upstream validation, feature flags,
deployment assumptions, reachability, and attacker prerequisites. Suspicious API
usage without a plausible reachable attack path is not enough.

### Pass 2: independent verification

The verifier receives candidate digests and bounded source excerpts, but not the
first pass's conversation. Both calls are `ReadOnly` and `Ephemeral`, forcing the
verifier to re-derive evidence rather than continuing the discoverer's argument.

The verifier may only confirm, reject, mark uncertain, or correct severity and
confidence for existing candidate ids. On `mow sec` it may also return
`evidence_fields` to correct or clear structured security evidence (source,
sink, reachability, and the other optional keys). Only keys present in the
verdict are changed; unknown keys are ignored with a report note (malformed
values for known keys still fail the run). It cannot introduce new findings.
Unknown, duplicate, or missing verdict ids are handled explicitly; malformed
model output is a hard error and cannot appear as a clean report.

### Mechanical validation

After model verification, mow validates paths and line ranges against the actual
scope, normalizes categories and severity, merges duplicate fingerprints,
redacts sensitive evidence, and suppresses unverified findings unless requested.
A truncated or empty scope remains visible in every output format.

## Structured security evidence

Security findings may include these optional fields in addition to the common
review schema:

| Field | Meaning |
|---|---|
| `source` | Attacker-controlled entry point |
| `sink` | Security-sensitive operation reached by the data/control flow |
| `sanitizers_considered` | Guards, validation, encoding, or framework protection checked |
| `reachability` | Reachable, conditional, or unknown, with the reason |
| `attacker_prerequisites` | Required role, network position, feature flag, or prior access |
| `evidence_limitations` | Configuration or runtime facts unavailable to static review |
| `attack_surface` | Exposed interface or component |
| `trust_boundary` | Boundary crossed by untrusted data or authority |
| `exploitability` | Practical exploitation assessment |
| `cwe` | Applicable CWE identifier |
| `evidence_level` | Strength of the read-only evidence |

Example:

```json
{
  "id": "sec-001",
  "severity": "high",
  "confidence": "high",
  "category": "path-traversal",
  "path": "internal/files/download.go",
  "start_line": 87,
  "end_line": 91,
  "source": "HTTP path parameter name",
  "sink": "os.Open(joinedPath)",
  "sanitizers_considered": "filepath.Clean without containment validation",
  "reachability": "public download route",
  "attacker_prerequisites": "network access; no authentication required",
  "evidence_limitations": "reverse-proxy policy unavailable",
  "evidence_level": "code-supported"
}
```

## Evidence levels

`mow sec` distinguishes the strength of a static claim:

| Level | Meaning |
|---|---|
| `suspected` | The candidate lacks a sufficiently traced static flow |
| `code-supported` | Some source, sink, reachability, or guard evidence exists, but uncertainty remains |
| `model-verified` | Pass two confirmed the finding (`verified: true`) **and** the structured source/sink/reachability claim is complete with no recorded limitations |

`model-verified` does **not** mean an exploit was run. A finding with complete
pass-one fields that pass two could not confirm stays at `code-supported` even
with `--include-unverified`. The current command can never emit
`execution-confirmed` because it has no execution authority.

## Output and CI

Shared formats, budgets, exit codes, and configurable budget caps are documented in [review.md](review.md). `mow sec` supports:

- `text` for human triage;
- `json` as the complete machine-readable report;
- `jsonl` for streaming and command-line processing;
- `sarif` for code-scanning integrations.

Structured evidence is preserved in JSON and SARIF properties. The text renderer
shows the highest-signal fields in source-to-sink order. Findings have stable
fingerprints for deduplication across runs.

A successful command means the review workflow completed, not that the code is
secure. An empty scope is reported as empty; malformed model output, scope
resolution failures, or exhausted review contracts fail instead of producing a
false clean result.

## Multi-reviewer discovery

The shared ensemble behavior is described in [review.md](review.md#group-review). Candidate discovery can use multiple models:

```bash
mow sec --reviewer gpt-5-mini,claude-sonnet-4 --reviewer-parallel 2
```

Each reviewer analyzes the same scope independently. Candidates are merged and
then passed through one verification stage (by default the first listed reviewer,
or `--verifier` when set). Optional finding extras record ensemble
provenance (`reviewer_count`, `reviewer_consensus`, `reviewers`) and pass-two
agreement on kept findings (`verifier_agreement`, e.g. `confirmed_independent`
when multiple reviewers surfaced the same fingerprint and the verifier confirmed
it). Rejected candidates are dropped and do not carry agreement extras. The
default remains one reviewer.

## Read-only safety contract

`mow sec` forces read-only, ephemeral prompts regardless of global configuration
or CLI power settings. Candidate and verifier engines skip extension setup and
extension lifecycle hooks during construction (`SkipExtensionSetup`,
`DisableExtensionHooks`); only user LLM config and pre-loaded
`extensions.review` budgets apply.

- write and edit are unavailable;
- bash and project execution are unavailable;
- extension tools, MCP, and ACP are not started for review engines and are not
  callable even when they declare `ReadOnly() true`;
- no persistent conversation is needed—the report is the artifact;
- no patch, test, reproducer, or exploit is generated or executed automatically.

Reports record `run.read_only: true` and `run.tool_policy: builtin_read_inspect_only`.
Each review pass uses `PromptOpts.AllowedTools` limited to `read`, `glob`, and
`grep` in addition to `ReadOnly`/`Ephemeral`. Extension, MCP, and ACP tools are
not exposed to the model and cannot execute, even when they declare
`ReadOnly() true`. `recall`, `understand_*`, write, edit, and bash are
excluded.

This boundary is intentional. Adding a quiet execution flag to `mow sec` would
make its trust posture ambiguous.

## Relationship to scanners

Static analyzers remain complementary. CodeQL, Semgrep, gosec, Trivy,
dependency scanners, secret scanners, and language-specific analyzers provide
specialized signals that a model should not claim to replace.

Future read-only work may let `mow sec` ingest and correlate external SARIF,
challenge stale or unreachable findings, enrich evidence, and emit one normalized
report. Scanner output remains evidence, not proof.

## Future commands

### `mow sec verify`

Execution-backed verification belongs in a separate command because it crosses
from passive inspection into code execution. A future implementation should:

- consume a `mow sec` JSON or SARIF report;
- select explicit finding ids;
- use a disposable checkout, worktree, or sandbox;
- run bounded tests, fuzzers, or minimal reproducers;
- disable ambient credentials and network access by default;
- record commands, outputs, exit status, timing, and sandbox restrictions;
- classify results as execution-confirmed, not reproduced, or inconclusive;
- never modify the primary workspace.

### `mow sec fix`

Remediation belongs in another explicit command because it grants mutation
authority. A future implementation should create a disposable worktree, patch
one selected finding, add a regression test, run verification again, and produce
a reviewable patch. It must never merge automatically.

The intended evidence ladder is:

```text
suspected
→ code-supported
→ model-verified
→ execution-confirmed                 (future: sec verify)
→ patched and regression-tested       (future: sec fix)
→ independently re-reviewed
```

Later stages append provenance to the original finding id/fingerprint rather
than erasing earlier uncertainty.

## Non-goals

- No automatic exploit execution in `mow sec`.
- No write or shell escape through configuration.
- No automatic patching or merging.
- No claim that an empty report proves security.
- No managed gateway or proprietary scanner requirement.

## See also

- [Shared review mechanics](review.md)
- [Documentation index](README.md)
