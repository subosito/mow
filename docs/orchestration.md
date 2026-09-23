# Goals, loops, schedulers, and workflows

Mow has several orchestration surfaces with deliberately different ownership and
lifetime. Choose the smallest one that matches the job.

| Surface | Who owns the next step? | Lifetime | Best for |
|---|---|---|---|
| Prompt / agent loop | Model, inside one `Engine.Prompt` | One turn | One bounded interactive task |
| `/loop` | Interactive host repeats one prompt | Current TTY/mowi process | Temporary polling while an operator is present |
| `/plan` + `/goal` | Model evolves a durable checklist | Durable under `$MOW_HOME/goals` | One novel objective whose steps must be discovered |
| `ext/workflow` | Host-owned, versioned procedure graph | Durable workflow run | Repeated procedures with typed phases and mandatory gates |
| systemd/cron/CI | Operating system or CI decides when | External and durable | Starting goals/workflows on a schedule |

The scheduler decides **when**. A workflow decides **which predefined procedure**.
A goal discovers **how to achieve one outcome**. None of these changes the inner
model/tool loop: every model worker is an ordinary bounded `Engine.PromptWith`.

## Prompt and inner loop

`Engine.Prompt` is one user turn. The model may call tools for several internal
rounds, but policy, cancellation, tool limits, context limits, and the run result
bound the operation. Hosts should not duplicate this loop.

Use a normal prompt when the work does not need durable orchestration:

```text
Explain this failing test and propose the smallest fix.
```

## Interactive `/loop`

`/loop` is host-memory scheduling in mowi and `mow tty`:

```text
/loop 5m --max 12 --for 1h --ephemeral check whether CI finished
/loop status
/loop now
/loop stop
```

It is intentionally temporary:

- the schedule disappears when the interactive host exits;
- it is not restored by `--continue`;
- fires never overlap and missed fires do not accumulate;
- a busy host skips a fire;
- each fire inherits the current Engine policy and model route.

Use systemd, cron, launchd, or CI when recurrence must survive the interactive
session. Scheduled jobs should name their goal/workflow/session explicitly; a
scheduler should not use bare `--continue`.

## `/plan` and `/goal`

A goal starts from a natural-language objective. The model may create and revise a
checklist, gather evidence, choose the next pending item, retry, ask the operator,
or stop blocked. Goal state persists and can be resumed.

```text
/plan fix-ci Make CI green without changing public behavior
/goal run fix-ci
```

`/plan` performs one read-only planning prompt and stores a pending goal. `/goal`
then executes the evolving objective. Goal is appropriate when the desired outcome
is known but the correct procedure is not.

Examples:

- investigate and fix an unfamiliar bug;
- implement a feature whose repository touch points are uncertain;
- refactor one subsystem;
- perform a one-off review and fix confirmed findings.

Goal checklist items are useful planning records, not typed dataflow contracts.
Its `phase`, worker, and verification projections help hosts display progress but
do not turn the checklist into a fixed workflow graph.

## `ext/workflow`

A workflow is a predefined, reusable, versioned procedure owned by host code. The
procedure—not a model—controls phase order and mandatory verification.

Example: a repository audit might always perform:

```text
inventory packages
  → fan out read-only package audits
  → validate typed findings
  → independently verify high-severity findings
  → deduplicate and rank
  → emit one report artifact
```

Models may investigate inside a node, but cannot skip verification, add a deploy
phase, raise budgets, or acquire capabilities not granted by the host.

Use workflow when all or most of these are true:

1. The same procedure runs repeatedly.
2. Phase order belongs to code, not the model.
3. Nodes exchange typed outputs or immutable artifact references.
4. Bounded fan-out/fan-in is part of the procedure.
5. Verification is an explicit mandatory phase.
6. Node retry and failure behavior must be deterministic.
7. A run must survive process interruption and resume from durable state.

Examples include a repeated repository audit, release qualification, a known API
migration, a compatibility matrix, and incident evidence collection.

### Initial workflow scope

The first implementation is intentionally smaller than a general workflow engine:

- registered Go definitions only;
- a versioned ordered phase graph;
- bounded `prompt` and deterministic `verify` nodes;
- durable local run state with atomic writes and one-run locking;
- typed JSON input/output with explicit byte limits;
- fail/stop semantics with no blind retry after uncertain side effects;
- CLI and slash surfaces owned by the optional extension;
- structured progress events for hosts.

It does **not** initially include:

- an arbitrary scripting language;
- model-generated workflow definitions;
- distributed queues or remote workers;
- deployment credentials or implicit production deployment;
- cron/calendar scheduling;
- cross-repository atomicity;
- unbounded dynamic fan-out.

Those capabilities require demonstrated procedures and stronger control-plane
contracts rather than speculative abstractions.

### Definition model

A host registers immutable definitions before creating runs:

```go
workflow.Register(workflow.Definition{
    Name:    "repository-audit",
    Version: "1",
    Phases: []workflow.Phase{
        workflow.PromptPhase("inventory", inventoryPrompt),
        workflow.VerifyPhase("verify", verifyFindings),
    },
})
```

A definition describes requested capabilities and budgets, but cannot grant them.
The effective worker policy is always the intersection with the Engine/host policy.
Project configuration cannot add power nodes, credentials, roots, or weaken gates.

### Phase contract

Each phase has a stable identifier and kind, plus bounded input and output:

```text
pending → running → succeeded
                  ↘ failed
                  ↘ blocked
```

A phase declares:

- its input source (run input or prior phase output);
- output size/schema contract;
- worker/tool policy;
- timeout and attempt limit;
- side-effect class;
- deterministic failure behavior.

The initial runtime executes phases sequentially. Future fan-out must preserve a
stable result order, total concurrency/cost ceilings, cancellation, and explicit
partial-failure policy.

### Durable state

Workflow run state belongs under:

```text
$MOW_HOME/workflows/runs/RUN_ID.json
```

The state records definition name/version, current phase, phase attempts, status,
bounded outputs, timestamps, and failure category. State is written atomically
before advancing to another phase. The runtime does not store credentials or raw
provider error payloads.

If a process exits while a phase is marked running, resume must not blindly replay
an uncertain side effect. The initial implementation marks it blocked for operator
reconciliation. Read-only/pure nodes may later opt into explicit idempotent retry.

### Artifacts

Large outputs do not belong inline in workflow state. A future artifact store should
be content-addressed and retain size, digest, provenance, access policy, and cleanup
metadata. The initial runtime keeps only small bounded JSON outputs; it does not
pretend to be an artifact database.

### Events and hosts

The extension emits optional workflow events such as:

```text
workflow.run.start
workflow.phase.start
workflow.phase.end
workflow.run.blocked
workflow.run.done
workflow.run.failed
```

ACP hosts may render them when advertised; generic clients may ignore them. Events
are presentation/observability projections, while the workflow store is the source
of truth.

## External scheduling

Mow does not need to run as a permanent workflow server. The normal unattended
shape is a oneshot process launched by mature scheduling infrastructure:

```text
systemd timer / cron / CI
            ↓
mow workflow run repository-audit --workspace /srv/project
            ↓
process exits; durable state and report remain
```

System schedulers own calendars, reboot persistence, missed-run policy, identity,
environment, process limits, and logs. `ext/workflow` owns the procedure and
recovery state.

A separate persistent control-plane host is justified only for multi-user remote
submission, centralized queues, admission control, authentication, or distributed
workers. That is a host/product concern, not a hidden mode of `ext/workflow`.

## Choosing the surface

Use this rule:

- **Prompt:** “Do this bounded task now.”
- **Plan/goal:** “I know the outcome; discover and execute the steps.”
- **Loop:** “Repeat this prompt temporarily while this UI remains open.”
- **Workflow:** “Run this known versioned procedure with mandatory phases.”
- **System scheduler:** “Start this work at a durable external time.”

A scheduler may launch a goal or workflow. A workflow node may use a bounded Engine
prompt. These layers compose; none should duplicate another layer's responsibility.
