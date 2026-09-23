# workflow

Optional host-owned runtime for predefined, versioned, repeatable procedures.
It orchestrates bounded `Engine.PromptWith` calls and trusted verifier functions;
it does not implement another agent loop or scheduler.

## Runtime API

```go
workflow.Register(workflow.Definition{
    Name: "repository-audit", Version: "1",
    Phases: []workflow.Phase{
        workflow.PromptPhase("inventory", "Return inventory JSON only", true, "read", "glob", "grep"),
        workflow.VerifyPhase("verify", verifyInventory),
    },
})

state, err := (&workflow.Runner{Engine: eng}).Run(ctx,
    "audit-2026-09", "repository-audit", "1", json.RawMessage(`{"root":"."}`))
```

Definitions are immutable process registrations. The initial runtime executes
phases sequentially. Prompt phases return one bounded JSON value. Verify phases
are trusted host callbacks. State persists under `$MOW_HOME/workflows/runs` by
default with atomic writes and crash-releasing run locks.

A process interrupted while a phase is marked running resumes as blocked; it is
never blindly replayed. The current runtime has no reconciliation command yet.

## Boundaries

- no arbitrary scripting language;
- no distributed workers or queue;
- no cron/calendar scheduler;
- no deployment node or credentials;
- no implicit capability grant—phase tool requests are intersected with Engine policy;
- no unbounded dynamic fan-out or large artifact store.

See [docs/orchestration.md](../../docs/orchestration.md) for goal/loop/workflow and
external scheduler boundaries.
