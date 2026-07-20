# AGENTS.md — working in the mow repo

**mow** is a headless Go agent harness: secure-by-default tool loop + sessions +
config. The **library** (`mow.Engine` / `mow.Run`) is the product API. UIs and
extra protocols live as packs under `ext/` or as **external hosts that import
this module** — do not re-add a TUI or product shell into this repo.

mow is **standalone**: one Go module, OpenAI/Anthropic-compatible HTTP. No other
repo, gateway product, or host is required to build, test, or run.

## Build, test, run

Requires **Go 1.26.4+** (pinned in `go.mod`). Prefer `devenv shell` (sets
`GOTOOLCHAIN=local` from devenv.lock).

```bash
devenv shell -- just verify    # go test ./... + go vet  — gate before commit
devenv shell -- just build     # → bin/mow
devenv shell -- go test -race ./...
```

No separate lint step. Format with `gofmt`. Do not invent Make/npm scripts.

## Request flow (spine)

`Engine.Prompt` / `PromptWith`: load config → tools + hooks → agent loop
(`internal/agent`) → LLM (`internal/llm`) → tools (`internal/tools`) with
policy jail (`internal/policy`) → session JSONL. Study **`engine.go`** +
**`engine_prompt.go`** first, then `internal/agent/loop.go`.

Events: `OnEvent` / `AddOnEvent` / `Emit` (`event.go`; `tool.end` includes `duration_ms`).
Cancel: `Engine.Cancel()` (fail-fast mid tool batch). Tool batches: `policy.max_parallel_tools` (default 8).

## Layout

| Path | Role |
|------|------|
| `mow` (root `*.go`) | Public Engine API (`engine_*.go`, `run.go`, `hooks.go`, `event.go`) |
| `cliutil/` | CLI flags → Engine (**not** a pack) |
| `packcfg/` | Decode `extensions.<name>` (**not** a pack) |
| `ext/` | Registration (`ext.go`) + packs: acp, rpc, goal, mcp, lsp, job |
| `internal/` | Implementation — **not** an integrator import surface |
| `cmd/mow/` | Thin CLI; blank-imports packs |
| `docs/` | architecture, harness, extensions |

Public vs internal: if integrators need something in `internal/`, re-export —
do not tell them to import `internal/`.

## Packs

- Stock binary links packs via blank import in `cmd/mow/main.go`.
- Remove import → subcommand/tools gone.
- Pack config: `extensions.<name>` via `Engine.Extension` or `packcfg.DecodeSection`.
- MCP/LSP only activate when configured (no config → no process).

## Conventions

- **Conventional Commits** (`feat(scope):`, `fix:`, `docs:`, `chore:`).
- Match surrounding style; scoped diffs; no drive-by refactors.
- Test non-trivial logic; table-driven like nearest `*_test.go`.
- Prefer stdlib; no new deps without a clear need.

## Security invariants (do not regress)

- Default tools: **read, glob, grep** only. Write/shell require `--allow-write` /
  `--allow-shell` or config.
- Workspace path jail on FS tools.
- No secrets in logs. Config paths under `$MOW_HOME` (default `~/.mow`).
- Optional HTTP attribution labels: `X-Mow-*` (ignored by plain providers).

## Gotchas

- Always `devenv shell --` for go/just when `devenv.nix` is present.
- CLI help shows `--long` flags; stdlib also accepts `-long`.
- Engine split: `engine.go` (New), `engine_prompt.go`, `engine_model.go`,
  `engine_control.go`, `engine_adapt.go`, `run.go` (Options/Run).
- This repo is headless (library + CLI + packs). Interactive UIs belong in
  external hosts that depend on `github.com/subosito/mow`.
- Tests isolate `$MOW_HOME` via `main_test.go` (`TestMain`); do not rely on the
  developer’s real `~/.mow`.

## Docs map

| Doc | Read when |
|-----|-----------|
| [docs/architecture.md](docs/architecture.md) | Public/internal, LLM endpoint model |
| [docs/harness.md](docs/harness.md) | Loop, tools, config, sessions |
| [docs/extensions.md](docs/extensions.md) | Packs, ACP, media, MCP/LSP |
