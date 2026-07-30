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

**System prompt:** `llm.system_prefix` (optional identity) → optional default
“You are mow” only if no prefix applies → harness rules (always) → project
AGENTS/skills. See `internal/contextload/harness.go`.

Events: `OnEvent` / `AddOnEvent` / `Emit` (`event.go`; `tool.end` includes `duration_ms`,
`run.end` includes token usage). Inline `<think>` CoT is stripped by the loop
(`internal/agent/think.go`) — committed history/sessions are always tag-free.
Cancel: `Engine.Cancel()` (fail-fast mid tool batch). Tool batches: `policy.max_parallel_tools` (default 8).

## Layout

| Path | Role |
|------|------|
| `mow` (root `*.go`) | Public Engine API (`engine_*.go`, `run.go`, `hooks.go`, `event.go`) |
| `cliutil/` | CLI flags → Engine (**not** a pack) |
| `packcfg/` | Decode `extensions.<name>` (**not** a pack) |
| `ext/` | Registration (`ext.go`) + packs: acp, rpc, goal, review, mcp, lsp, job, ops, proc, cmdhook |
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

- Match surrounding style; scoped diffs; no drive-by refactors.
- Test non-trivial logic; table-driven like nearest `*_test.go`.
- Prefer stdlib; no new deps without a clear need.

## Public samples (OSS)

This module is **open source**. Anything a stranger reads on GitHub must not
imply a private fleet, host product, or in-house gateway.

| Do | Don't |
|----|--------|
| Current public provider ids (`gpt-5-mini`, `gpt-5.4-mini`, `deepseek-chat`, `claude-sonnet-4`, `gemini-2.5-flash`) | Stale ids (`gpt-4o`, `gpt-4.1`, …), host-only catalog nicknames, private revs, or peer names that only exist on the home fleet |
| Generic roles (`peer-agent`, `api`, `gateway`, `embedders`, `host UIs`) | Private product binaries, TUI host names, ops profile names from the home fleet |
| “OpenAI-compatible gateway”, “when the peer CLI accepts `--reasoning-effort`” | Naming a sibling monorepo product as if it were part of mow |
| `http://127.0.0.1:PORT/v1`, `https://api.openai.com/v1` | Home-lab ports, real keys, `$HOME` paths to other repos |
| `facet` / `efforts` described as optional **gateway** metadata | Documenting a specific private catalog product as required |

**Where this applies:** `docs/`, `README.md`, `internal/config/mow.yaml.example`,
CLI help strings, public Go doc comments, and **test fixtures that look like
examples** (ids in `*_test.go` that readers treat as “how to configure”).

**OK to keep:** real public wire names (`openai-responses`, `anthropic-messages`),
stdlib/third-party protocol brands the code actually speaks, and implementation
comments that describe wire fields without advertising a private stack.

**Before commit:** if the diff touches docs, examples, or fixture model ids,
re-read for host/fleet names **and** stale model generations. Prefer current
public ids (GPT-5 family, current Claude/Gemini/DeepSeek lines) over last year’s
defaults. Behavior that supports a gateway prefix (e.g. Gemini-family heuristics)
may stay; **wording and samples** stay generic and up to date.

## Commits

**Always use [Conventional Commits](https://www.conventionalcommits.org/)** when
creating a git commit in this repo (agents and humans). Informal subjects
(`run`, `updates`, `wip`, `fix stuff`) are not acceptable.

Format:

```
type(optional-scope): short imperative subject

Optional body: why this change, not a file list.
```

| Rule | Detail |
|------|--------|
| Types | `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `perf` (pick one) |
| Scope | Prefer when clear: `llm`, `agent`, `goal`, `job`, `engine`, `cli`, `mcp`, `rpc`, `tools`, `config`, pack name, … |
| Subject | Imperative, lowercase after the colon, no trailing period, ~72 chars max |
| Body | Blank line after subject; explain *why* when non-obvious |
| Splits | One logical change per commit when practical; do not dump unrelated work into one subject |

Examples:

```
feat(llm): add openai-responses wire
fix(goal): raise max_steps on resume via --max-steps
docs: document openai-responses in harness
chore: remove obsolete review notes
```

Gate: `devenv shell -- just verify` before commit when the change is non-trivial.
Also apply **Public samples (OSS)** above when the commit includes docs or fixtures.

## Security invariants (do not regress)

- Default tools: **read, glob, grep** only. Write/shell require `--allow-write` /
  `--allow-shell` or config.
- Workspace path jail on FS tools (optional `policy.extra_roots` / repeatable
  `--extra-root` expand the jail — host/CLI only).
- Workspace trust is out-of-band (`$MOW_HOME/trusted`, `mow trust`) — never a
  marker inside the workspace. Project config may not set credentials,
  `llm.base_url`, headers, `session.dir`, power tools, or `extra_roots`.
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
| [docs/embedding.md](docs/embedding.md) | Embedding in Go: options, events, custom tools/providers, hooks, sessions |
| [docs/harness.md](docs/harness.md) | Loop, tools, config, sessions |
| [docs/extensions.md](docs/extensions.md) | Packs, ACP, media, MCP/LSP |
| [docs/review.md](docs/review.md) | `mow review` / `mow sec`: two-pass workflow, report schema, exit codes |
