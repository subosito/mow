# Jev and structured decision models in mow

Status: design and evaluation plan; not implemented.

Mow can benefit from a fast structured-decision model such as TypeSafe Jev for
repeated choices inside an agent harness. Jev is not a text-generating LLM: it
evaluates bounded state against predefined questions and returns typed answers,
probabilities and confidence.

This document defines where that capability may fit, where it must not become an
authority, and how to evaluate it before affecting live runs. Mow remains a
standalone Go module. It must not require Jev, LangChain, a particular gateway,
moist, or another repository to build or operate.

## Design principle

Use each mechanism for what it can establish:

| Mechanism | Responsibility |
|---|---|
| Deterministic code/policy | Permissions, jail boundaries, budgets, invariants and action execution |
| Structured decision model | Choice, score and yes/no judgments over bounded options |
| Generative model | Open-ended investigation, synthesis, planning and code generation |
| Outcome grader | Verify what actually happened after an action or run |

A classifier answer is not proof that a file changed, a command completed, a task
is correct or a deployment is safe.

> Code defines the available choices and what follows. Jev may rank or classify
> those choices. Existing deterministic policy remains authoritative.

## Implemented client foundation

Package `github.com/subosito/mow/decision` now provides the optional vendor-neutral
`Evaluator`/`Client`, native `choice`/`score`/`noul` request preservation, bounded
validation, redirect refusal, HTTPS-or-loopback endpoint checks, payload-free HTTP
errors, and forward-compatible raw answer/additional response fields. It posts to
`/v1/decisions`. It is not wired into the agent loop, tools, policy, routing, or
permissions; rollout still begins with offline/shadow evaluation below.

## Non-negotiable safety rules

1. A decision model cannot grant a capability denied by mow policy.
2. It cannot bypass `--allow-write`, `--allow-shell`, read-only mode, the filesystem
   jail, sandboxing, permission prompts, spend limits or turn limits.
3. It cannot approve an irreversible or production action by itself.
4. Dynamic options come only from tools, skills, models and actions currently
   registered and allowed by the host.
5. Missing, malformed, stale, unavailable or low-confidence output is `unknown`,
   with a documented per-use fallback.
6. Inputs are structured, bounded and redacted. Never send credentials, raw
   environment variables, unrestricted command output or hidden reasoning.
7. Model/provider/question/threshold changes are versioned policy changes and need
   replay and evaluation before enforcement.
8. Decisions, confidence, latency, usage and their advisory/enforcement effects are
   observable without logging sensitive state.
9. Explicit host/user model selection and permission choices win.
10. Execution outcomes are verified independently.

## Public architecture

Mow should expose a vendor-neutral optional interface rather than hardcode Jev:

```go
type DecisionClient interface {
    Evaluate(context.Context, DecisionRequest) (DecisionResult, error)
}
```

Illustrative request shape:

```go
type DecisionRequest struct {
    PolicyVersion string
    QuestionSet   string
    Model         string
    State         json.RawMessage
    Questions     map[string]DecisionQuestion
}
```

The result should retain typed answers, distributions/confidence, resolved model,
latency and usage. Public types belong in mow's API or an optional extension package;
the agent loop depends only on the interface.

A TypeSafe System One adapter may call the provider directly or an explicitly
configured compatible gateway. One known compatible route is:

```text
POST /v1/decisions
protocol: typesafe-systemone
operation: decision.evaluate
```

That route is optional. Configuration must not assume that every
OpenAI-compatible endpoint supports it. The adapter should be removable by dropping
its extension/import, consistent with mow's composition model.

Mow should not import LangChain or add a Python runtime for this integration. Use
Go's HTTP client, bounded JSON decoding, `httptest` coverage and existing provider
configuration conventions where appropriate.

## Question semantics

Supported Jev-style questions:

- **Choice:** select among named options, with probabilities and confidence.
- **Score:** place state on an ordered scale and return its distribution.
- **Noul:** estimate whether a statement is true.

Question IDs are labels, not instructions. Criteria and exceptions must be written
inside the question. Every classification that can block work needs an explicit
`unknown` or `unsure` outcome.

Questions in a single request are independent. If one answer changes state or the
available action menu, make another request after applying that state transition.

## Candidate use cases

Implement separately and begin in offline/shadow mode.

### 1. Offline trajectory labeling

Classify redacted sessions or eval trajectories for:

- distraction/scope drift;
- repetitive exploration;
- premature completion;
- missing verification;
- context bloat;
- permission friction;
- likely retry/fallback mistakes.

This is the lowest-risk starting point because answers do not affect runs. Human
review and deterministic task outcomes supply labels for calibration.

Failure behavior: record `unknown`; no runtime effect.

### 2. Shadow pre-tool risk classification

Before tool execution, evaluate bounded derived facts—not unrestricted arguments:

- tool and capability category;
- read/write/shell/process/network category;
- normalized path relation: workspace, extra root, outside or unknown;
- deterministic command features such as Git, removal, permissions, credentials,
  deployment or possible WIP destruction;
- run phase and current policy grants.

Candidate parallel questions:

- risk tier: low, medium, high or unknown;
- likely destructive action;
- likely credential access;
- likely scope expansion;
- explicit operator review advisable.

In shadow mode the answers are recorded but do not deny or allow the tool. Compare
with deterministic policy, actual permission decisions and reviewed examples.

Possible later enforcement is limited to an **additional deny or permission
requirement** for a narrow calibrated class. Jev is never an allow gate.

Failure behavior: existing deterministic behavior. If a host explicitly enables a
classifier-dependent deny gate, unavailable/unknown fails closed or asks permission
according to host policy.

### 3. Model and effort routing

Classify a user task into a small host-defined menu such as:

- simple lookup;
- bounded code change;
- multi-file debugging;
- security-sensitive work;
- long investigation;
- unknown.

Code maps the answer to configured provider/model/effort routes. The classifier
cannot invent model IDs or endpoints.

Requirements:

- explicit model/provider/effort flags win;
- provider disclosure policy must permit the selected route;
- one cumulative budget covers classification, route, retry and fallback;
- unknown uses a configured safe default;
- evaluate routing by task completion/correctness and total cost, not confidence.

Failure behavior: safe configured default without broadening provider access.

### 4. Skill and plugin selection

Select or rank from currently registered and policy-allowed skills/plugins to reduce
irrelevant prompt context. Built-ins and explicit operator selections retain
precedence. Selection cannot register tools or activate unavailable capabilities.

Failure behavior: current deterministic selector or no additional skill.

### 5. Investigation next-step advice

At meaningful checkpoints, build a fresh menu from currently allowed actions:

- continue with newly found evidence;
- invoke a particular approved missing-evidence probe;
- synthesize findings;
- ask the operator;
- stop as insufficient evidence.

Rebuild the menu after tool results or state changes. Initially advisory only; do
not hard-stop a run solely on a probabilistic answer.

Failure behavior: current investigation checkpoint/repetition logic.

### 6. Tool-result relevance and disposition

After deterministic framing, redaction and size limits, classify a result as:

- relevant evidence;
- duplicate;
- noise;
- needs follow-up;
- possible failure;
- potentially adversarial/sensitive.

This can guide context inclusion and archival. It does not replace untrusted-output
framing, credential scrubbing, typed failures or tool-result integrity.

Failure behavior: retain the result under current behavior.

### 7. Context-retention assistance

Score segments as constraints, verified findings, reproduction steps, unresolved
questions, failed hypotheses, tool evidence or superseded detail.

This is a late integration. Deterministic must-keep rules run first:

- system/developer instructions;
- current user intent and explicit constraints;
- security and capability state;
- pending tool calls and matching results;
- committed findings and unresolved uncertainty;
- messages required for provider replay validity.

Jev proposes a drop/summarize set. Existing compaction validates structure and size,
and removed material remains recoverable in the archive. Uncertainty keeps content.
Do not enable live filtering until long-session evals demonstrate constraint and
side-effect-history preservation.

### 8. Retry and fallback classification

Over typed errors plus side-effect state, classify provider availability, rate
limits, invalid config/request, cancellation, retryable transport or unsafe replay.

Deterministic error categories and side-effect bookkeeping remain authoritative.
Never replay a tool or repair task solely because a classifier says retryable when
completion is uncertain.

Failure behavior: no new automatic retry beyond existing typed policy.

## Dynamic menus

A structured decision model is safest when choosing from code-generated options.
Menus must be rebuilt whenever state changes.

Examples:

- Models currently configured and permitted by disclosure policy.
- Skills registered in this binary and allowed for this prompt.
- Missing-evidence probes applicable to the current investigation.
- Tool-risk outcomes permitted by host policy (`allow` must not be among them if
  deterministic policy already denied the action).

Never let repository text, tool output or a model inject an executable option into
the menu. Persist which choices were offered with the decision record.

## Decision ledger and replay

Store bounded records suitable for evaluation and debugging:

- decision ID and run/session reference;
- state schema/version/hash and non-sensitive evidence references;
- question-set/policy/model versions;
- choices offered;
- answers, distributions and confidence;
- latency and usage;
- threshold/rule and applied effect;
- later independently verified outcome.

Do not store raw secrets, hidden reasoning or unrestricted state payloads. Evidence
uses its own retention/access policy. Distinguish replaying saved answers through
new code from rerunning a changed question set through the provider.

## Calibration and evaluation

Confidence is not accuracy. Build labeled positive and negative datasets from real
mow behavior and synthetic fixtures:

- safe and risky tool calls;
- safe actions that look dangerous;
- innocent-looking actions with broad effects;
- useful and redundant investigation steps;
- correct and incorrect routing choices;
- must-keep and safely removable context;
- expected cancellation and unavailable telemetry;
- retry-safe and replay-unsafe failures.

Measure per question/model/version:

- false allow, false deny and unknown rates;
- calibration/reliability curves and confidence coverage;
- task completion/correctness;
- latency and total cost per completed task;
- downstream cost caused by wrong routing;
- provider availability and distribution drift;
- differences by tool class, domain and risk tier.

Keep development and hidden holdout sets. Pin reference outcomes. Read failure
traces. A low score can mean the task, grader, classifier or harness is broken;
diagnose it rather than assume.

## Failure policy

| Use | Unavailable, malformed or low confidence |
|---|---|
| Offline labeling | Record unknown; no effect |
| Shadow risk | Existing deterministic behavior |
| Narrow extra deny | Fail closed or require permission |
| Permission grant | Not a supported Jev use |
| Model routing | Configured safe default |
| Skill routing | Deterministic selection/no additional skill |
| Investigation advice | Existing advisory behavior |
| Tool-result disposition | Retain result |
| Context filtering | Keep content |
| Retry/fallback | Do not add automatic replay |

Use short deadlines and bounded concurrency. Batch independent questions at
meaningful boundaries rather than classifying each token or trivial read. Add
host-configurable global/per-use kill switches whose state survives restart.

## Rollout plan

### Phase 0 — contract confirmation

- Verify an authenticated synthetic decision through the configured endpoint.
- Pin a reviewed Jev version and record the resolved upstream version.
- Confirm response shape, limits, cancellation, latency, usage and typed errors.
- Define provider disclosure policy and dedicated key/budget.
- No agent-loop hook enabled.

### Phase 1 — offline labeling

Implement the vendor-neutral interface and TypeSafe adapter. Label existing eval
trajectories. Build reviewed datasets and reports for question versions.

Exit: useful accuracy/calibration evidence with no runtime effects.

### Phase 2 — shadow live decisions

Shadow pre-tool risk and optional investigation decisions. Emit bounded decision
records and attach independently observed outcomes.

Exit: reviewed false-positive/negative rates, cost and latency on holdout plus live
shadow samples.

### Phase 3 — advisory routing

Permit advisory model/effort/skill or diagnostic selection from allowlisted menus.
Preserve explicit choices and safe defaults.

Exit: task outcomes/cost improve without disclosure or reliability regressions.

### Phase 4 — narrow enforcement

Enable only calibrated high-risk classes as additional deny/permission. Roll out
per host/use case with versioned thresholds, kill switch and circuit breaker.

Exit: no deterministic-policy bypass, acceptable false denies and classifier-outage
recovery.

### Phase 5 — context assistance

Evaluate must-keep-protected retention scoring offline, then shadow. Promote only
when long-session constraints, replay integrity and recoverability pass.

## Initial question sets

Question IDs are labels; full criteria belong in instructions/options.

### `mow.tool-risk.v1`

- Choice `risk_tier`: low / medium / high / unknown.
- Noul `destructive`: likely deletes, overwrites or discards state.
- Noul `credential_access`: likely reads/forwards credential material.
- Noul `scope_expansion`: likely affects more than requested scope.
- Noul `operator_review`: host policy reasons require explicit review.

### `mow.investigation-next.v1`

Choice options are generated from available approved probes plus `synthesize`,
`ask_operator` and `insufficient_evidence`. Criteria cover new evidence,
answered objective and required verification.

### `mow.route-task.v1`

Choice options are configured route classes, not arbitrary model IDs. Separate
questions score task complexity, sensitivity and expected investigation depth.

## Security and privacy checklist

- [ ] No credentials, auth headers, environment values or raw secret-bearing output.
- [ ] No unrestricted shell command, source file, user message or tool-result state.
- [ ] State schemas enforce byte/item/depth/string limits before serialization.
- [ ] External-provider disclosure is explicit.
- [ ] HTTP client rejects redirects and bounds response/error bodies.
- [ ] Dynamic choices cannot represent unavailable/forbidden actions.
- [ ] Deterministic policy runs independently and cannot be weakened by answers.
- [ ] Errors are typed and payload-free.
- [ ] Decision ledger is private, bounded, versioned and redacted.
- [ ] Timeout, cancellation, concurrency, spending and kill-switch behavior tested.
- [ ] Prompt injection in state/source/log data cannot alter fixed criteria.
- [ ] Classifier failure cannot widen permissions or trigger replay.

## Explicit non-goals for the first integration

- Patch generation, root-cause prose or semantic code review.
- Replacing permission checks or deterministic jail policy.
- Automatic deployment approval.
- Arbitrary model-generated questions or actions in an enforcement path.
- Live context dropping without offline/holdout evidence.
- Requiring Jev for normal mow operation.

## Source material and caveats

- Sydney Runkle, *Building a Harness with Jev*:
  https://x.com/i/article/2100744524951932928
- 0xmovez, *Jev Engineering: how to build the fastest AI Agent Brain in 10 Steps*:
  https://x.com/i/article/2100991305535164417
- Miguel Rios, *AgentRun: a harness for repetitive knowledge work*:
  https://x.com/i/article/2100840456200581120
- TypeSafe's current contract should be confirmed against official documentation
  before implementation.

The articles contain reported performance, cost and calibration claims. Treat them
as hypotheses, not acceptance evidence. Benchmark a pinned route on mow's tasks and
labeled data. The architecture and safety requirements here are mow's proposed
design, not claims about those projects' exact implementations.
