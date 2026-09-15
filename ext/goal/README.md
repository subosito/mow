# goal

Durable outer loop over `Engine.Prompt`: checklist state, evidence, budgets, optional parallel nodes, worktree workers, and process tools. Core stays one Prompt / one tool loop; this extension only orchestrates.

## Link

```go
import _ "github.com/subosito/mow/ext/goal"
```

Stock `cmd/mow` blank-imports this package. Goal-step tools are **not** globally registered. They are injected via `PromptOpts.ExtraTools` for the in-flight step only.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `goal` (`mow goal`) |
| Slash | `/goal` |

Subcommands: `list`, `new`, `run`, `status`, `reset`, `delete`.

```bash
mow goal new --id fix-ci --goal "Make CI green"
mow goal run --id fix-ci --allow-write --allow-shell
mow goal run --goal "Make CI green"
mow goal run --id fix-ci --answer "use option B"
mow goal status --id fix-ci
```

State lives under `$MOW_HOME/goals`. `--max-steps N` is the outer Prompt budget (default 16, ceiling 64).

## Config

None. There is no `extensions.goal` section.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
