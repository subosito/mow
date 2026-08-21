# focus

Soft explore guards: nudge the agent away from re-read/inventory thrash,
destructive discards of uncommitted work, and endless explore-only turns.

## Link

```go
import _ "github.com/subosito/mow/packs/focus"
```

Stock `cmd/mow` blank-imports this package. It rides the public hook surface
(`ext.RegisterPreToolSource` / `RegisterPostToolSource` /
`RegisterAfterTurnDecisionSource`) and imports no `internal/…`.

## Commands and tools

No CLI, no tools, no slash commands. The pack installs hooks at `BeforeNew`:

1. view caps — the same `read` window (path+offset+limit), or `bash cat/sed/head/tail`; the call still runs
2. inventory caps — repeated `git status`/`ls`/`find` degrade, then refuse; `git log`/`show`/`diff` and `rg`/`grep` key on args
3. a soft block on destructive `git`/`rm` that would discard uncommitted work
4. productive bash (`go test`, `go build`, `git commit`, …) resets the streak
5. a nag after N consecutive explore-only turns

All decisions are soft (warn/nudge), except the destructive-discard block.

## Config (`extensions.focus`)

All keys optional; defaults reproduce the pre-extraction core behavior.

```yaml
extensions:
  focus:
    explore_warn_every: 6       # nag after N explore-only turns
    reread_limit: 1             # repeats of the same view before results degrade
    inventory_limit: 2          # inventory calls before results degrade
    hard_inventory_limit: 4     # inventory calls before refusal
    degraded_result_limit: 2000 # cap (chars) on a degraded result body
```

A malformed section falls back to defaults rather than failing Engine
construction — the guards are an advisory lane, not a gate.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — explore guards
- [docs/harness.md](../../docs/harness.md)
