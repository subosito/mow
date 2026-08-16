# AGENTS.md — working in the mow repo

mow is a public library plus detachable extensions and optional hosts. The
interactive TUI is the Rust `mowi` sibling project, which drives `mow rpc`;
do not move TUI dependencies into the root module or `internal/engine`.

mow is **standalone**: a Go workspace (root, packs, and packs/otel modules), OpenAI/Anthropic-compatible HTTP. No other
repo, gateway product, or host is required to build, test, or run. The Rust
`mowi` sibling project is an external TUI that drives `mow rpc`.

## Build, test, run

Requires **Go 1.26.4+** (pinned in `go.mod`). Prefer `devenv shell` (sets
`GOTOOLCHAIN=local` from devenv.lock).

```bash
devenv shell -- just verify    # vet + go test -race + build  — gate before commit
devenv shell -- just verify-ci # same, but with no credentials and an empty MOW_HOME
devenv shell -- just build     # → bin/mow
devenv shell -- just test      # fast inner loop (no race detector)
```

`just verify` mirrors `.github/workflows/ci.yml` step for step, so a green
verify means a green CI. Run it **after changes and before commit** — the race
detector is part of the gate because CI runs `-race`, and unsynchronized test
helpers pass plain `go test`. If you add a CI step, add it to `verify` too.

**Before pushing, run `just verify-ci`.** CI has no API key and no `~/.mow`;
a developer box has both, so tests that build an Engine can pass locally and
fail on CI. `verify-ci` runs the suite with credentials unset and a throwaway
`MOW_HOME`. Packs whose tests construct an Engine need a `TestMain` that pins
`MOW_HOME`, `MOW_API_KEY`, and `MOW_MODEL` (use `testutil.RunWithProvider`; see
`packs/job`).

**A test may not assume anything is listening on a port.** CI runs no
collector, database, or peer; a hard-coded `127.0.0.1:PORT` passes only on a
box that happens to run that service. Start an `httptest.NewServer` and use
its URL. `verify-ci` does not sandbox the network, so this one is on review.

No separate lint step. Format with `gofmt`. Do not invent Make/npm scripts.

## Request flow (spine)

`Engine.Prompt` / `PromptWith`: load config → tools + hooks → agent loop
(`internal/agent`) → LLM (`internal/llm`) → tools (`internal/tools`) with
policy jail (`internal/policy`) → session JSONL. Study
**`internal/engine/engine.go`** + **`internal/engine/engine_prompt.go`** first,
then `internal/agent/loop.go`.

**System prompt:** `llm.system_prefix` (optional identity) → optional default
“You are mow” only if no prefix applies → harness rules (always) → project
AGENTS/skills. See `internal/contextload/harness.go`.

Events: `OnEvent` / `AddOnEvent` / `Emit` (`internal/engine/event.go`; `tool.end`
includes `duration_ms`, `run.end` includes token usage). Inline `<think>` CoT is
stripped by the loop (`internal/agent/think.go`) — committed history/sessions
are always tag-free. Cancel: `Engine.Cancel()` (fail-fast mid tool batch). Tool
batches: `policy.max_parallel_tools` (default 8).

## Layout

Source of truth for modules and public/internal: [docs/architecture.md](docs/architecture.md).

| Path | Role |
|------|------|
| `mow.go` | Thin public aliases/wrappers for `Engine`, `Run`, events, hooks, providers |
| `internal/engine/` | Engine implementation and behavior tests |
| `cliutil/` | CLI flags → Engine (**not** a pack) |
| `extcfg/` | Decode `extensions.<name>` (shared by extensions and packs) |
| `testutil/` | Shared test helpers (e.g. pin `$MOW_HOME` for `TestMain`) |
| `ext/` | Registration (`ext.go`) + core extensions: acp, mcp, proc, rpc, cmdhook, eval |
| `packs/` | Optional packs (separate Go module `github.com/subosito/mow/packs`): goal, review, ops, lsp, job, contextsink |
| `packs/otel/` | OTLP export (nested submodule `github.com/subosito/mow/packs/otel`; config-driven) |
| `internal/` | Implementation — **not** an integrator import surface |
| `cmd/mow/` | Sole full pack host; blank-imports packs |
| `docs/` | architecture, harness, extensions |

Public vs internal: if integrators need something in `internal/`, re-export —
do not tell them to import `internal/`.

## Packs

- Stock binary links packs via blank import in `cmd/mow/main.go`.
- Core packs live in `ext/` (part of the root module): acp, mcp, proc, rpc,
  cmdhook, eval.
- Optional packs live in `packs/` (separate Go module
  `github.com/subosito/mow/packs`): goal, review, ops, lsp, job.
- `go.work` wires the root module and `packs/` together for local dev.
- Remove import → subcommand/tools gone.
- Extension config: `extensions.<name>` via `Engine.Extension` or `extcfg.DecodeSection`.
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
  `--extra-root` expand the jail — host/CLI only; fixed at `mow.New`).
  The jail covers **FS tools only** — `bash` is not path-jailed. It is a
  guardrail against accidental scope creep, not containment against a hostile
  model; `--allow-shell` is the real trust decision. See
  [docs/harness.md](docs/harness.md) § Extra FS roots.
- Workspace trust is out-of-band (`$MOW_HOME/trusted`, `mow trust`) — never a
  marker inside the workspace. Project config may not set credentials,
  `llm.base_url`, headers, `session.dir`, power tools, or `extra_roots`.
- No secrets in logs. Config paths under `$MOW_HOME` (default `~/.mow`).
- Optional HTTP attribution labels: `X-Mow-*` (ignored by plain providers).

## Gotchas

- Always `devenv shell --` for go/just when `devenv.nix` is present.
- Engine split under `internal/engine/`: `engine.go` (New), `engine_prompt.go`,
  `engine_model.go`, `engine_control.go`, `engine_adapt.go`, `run.go` (Options/Run).
- The root module stays headless and lean. The Rust `mowi` sibling project is
  the interactive UI and drives `mow rpc`; never import TUI dependencies into
  the root module.
- Tests isolate `$MOW_HOME` via `TestMain` + `github.com/subosito/mow/testutil`;
  do not rely on the developer’s real `~/.mow`.

## Docs map

| Doc | Read when |
|-----|-----------|
| [docs/architecture.md](docs/architecture.md) | Public/internal, LLM endpoint model |
| [docs/embedding.md](docs/embedding.md) | Embedding in Go: options, events, custom tools/providers, hooks, sessions |
| [docs/harness.md](docs/harness.md) | Loop, tools, config, sessions |
| [docs/extensions.md](docs/extensions.md) | Packs, ACP, media, MCP/LSP |
| [docs/review.md](docs/review.md) | `mow review` / `mow sec`: two-pass workflow, `--reviewer` / `--verifier`, report schema, exit codes |
