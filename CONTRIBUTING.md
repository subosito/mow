# Contributing to mow

Thanks for hacking on mow. This page is the human-oriented orientation;
[AGENTS.md](AGENTS.md) is the same ground truth written for AI coding agents
(build, spine, security invariants, gotchas) — keep both in sync when you
change a rule.

## The one thing to internalize

**The library is the product.** `mow.Engine` / `mow.Run` is the API people
integrate against; the CLI and every pack are thin shells over it. So:

- New capability an integrator needs → it goes on the **public `mow` package**
  (or a pack), never buried where you'd have to tell someone "just import
  `internal/`".
- Implementation detail → `internal/*`. If an integrator ends up needing it,
  re-export it deliberately rather than leaking the internal path.
- A frontend, protocol, or heavy optional feature → a **core extension** under
  `ext/` or an **optional pack** under `packs/` (blank-imported into `cmd/mow`),
  or an external host that imports mow. The Rust `mowi` sibling project is the
  interactive TUI and drives `mow rpc`.

Read [docs/architecture.md](docs/architecture.md) once for the public/internal
line, then [docs/embedding.md](docs/embedding.md) to see the API from an
integrator's seat — most "where does this go?" questions answer themselves
after those two.

## Build, test, verify

Requires **Go 1.26.4+** (pinned in `go.mod`). `devenv shell` sets
`GOTOOLCHAIN=local`.

```bash
devenv shell -- just verify   # go test ./... + go vet — the gate before every commit
devenv shell -- just build    # → bin/mow
devenv shell -- go test -race ./...   # run when touching the loop, hooks, or sessions
# Plain Go, no devenv/nix:
go test ./... && go vet ./...
```

No separate lint step; format with `gofmt`. Don't invent Make/npm scripts.

## Where things live

Layout and module boundaries: [docs/architecture.md](docs/architecture.md)
(source of truth). Quick map for day-to-day edits:

| You're changing… | Look at |
|------------------|---------|
| The agent loop, turns, compaction | `internal/agent/loop.go`, `internal/agent/think.go` |
| The public API surface | `mow.go` (aliases) + `internal/engine/` |
| A built-in tool or the path jail | `internal/tools/`, `internal/policy/` |
| Config, env, trust | `internal/config/`, `internal/contextload/` |
| An LLM wire | `internal/llm/` |
| A core extension (acp, mcp, proc, rpc, cmdhook, eval) | `ext/<name>/` |
| An optional pack (goal, review, ops, lsp, job) | `packs/<name>/` |
| OTLP export | `packs/otel/` |
| Interactive TUI | Rust `mowi` sibling project over `mow rpc` |

Engine construction is `internal/engine/engine.go` (`New`); prompt/model/control
split across `engine_prompt.go`, `engine_model.go`, `engine_control.go`,
`engine_adapt.go`, `run.go`.

## House style

- **Scoped diffs.** Touch only what the change requires — no drive-by refactors,
  renames, or dependency bumps bundled into an unrelated PR.
- **Match the surrounding code.** mow leans on the stdlib; two runtime deps
  (pty, yaml) is the whole budget. A new dependency needs a clear, stated reason.
- **Test non-trivial logic**, table-driven in the style of the nearest
  `*_test.go`. Tests isolate `$MOW_HOME` via `TestMain` + `testutil` — never
  touch the developer's real `~/.mow`.
- **Show evidence.** "Done" means test output or observed behavior, not a claim.

## Security invariants

Load-bearing rules live in [AGENTS.md § Security invariants](AGENTS.md#security-invariants-do-not-regress)
— do not regress them without an explicit, discussed reason.

## Commits

**[Conventional Commits](https://www.conventionalcommits.org/) are required** —
`feat(scope): …`, `fix: …`, `docs: …`, `chore: …`, `refactor:`, `test:`,
`perf:`. Informal subjects (`run`, `wip`, `fix stuff`) are rejected. The body
explains *why*, not a file list. One logical change per commit. Full rules:
[AGENTS.md § Commits](AGENTS.md#commits).

Run `devenv shell -- just verify` before committing anything non-trivial.
