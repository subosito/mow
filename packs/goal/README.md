# goal

Durable outer loop over `Engine.Prompt`: checklist state, evidence, budgets, optional parallel nodes, worktree workers, and process tools. Core stays one Prompt / one tool loop; this pack only orchestrates.

## Link

```go
import _ "github.com/subosito/mow/packs/goal"
```

`cmd/mowx` blank-imports this package; lean `cmd/mow` does not. The Rust `mowi` sibling project
shows slash `/goal` over `mow acp` when this pack is linked. Job depends on goal.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `goal` (`mow goal`) |
| Slash | `/goal` (also listed by `slash.list`) |

Subcommands: `list`, `new`, `run`, `status`, `reset`, `delete`. The interactive
form runs against the current session Engine; `run` and one-shot goals emit
`graph.goal.*` notifications on the ACP session/update stream.

Goal-step tools are **not** globally registered. They are injected via `PromptOpts.ExtraTools` for the in-flight step only:

| Tool | Role |
|---|---|
| `goal_report` | finish / continue / plan / evidence |
| `goal_process_start` | goal-scoped background process |
| `goal_process_status` | |
| `goal_process_stop` | |

```bash
mow goal new --id fix-ci --goal "Make CI green"
mow goal run --id fix-ci --allow-write --allow-shell
mow goal run --goal "Make CI green"
mow goal run --id fix-ci --answer "use option B"   # unblock a blocked goal
mow goal status --id fix-ci
mow goal reset --id fix-ci
mow goal delete --id fix-ci [--force]
```

`--max-steps N` is the outer Prompt budget (default 16, hard ceiling 64); on resume it raises stored `max_steps` when larger.

State lives under `$MOW_HOME/goals` (override with `--dir`). One run per goal id across processes via `<id>.run.lock`. `--force` on delete only bypasses leftover `StatusRunning` after the lock is acquired.

## Config

None. There is no `extensions.goal` section.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — Goal pack
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
