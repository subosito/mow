# AGENTS.md — working in the mow repo

mow is a **standalone** Go workspace (root + `packs/`): OpenAI/Anthropic-compatible HTTP. No gateway or sibling repo is required to build, test, or run. The Rust **mowi** TUI is external and drives `mow acp` — never import TUI deps into the root module or `internal/engine`.

## Build, test, run

Requires **Go 1.26.4+** (`go.mod`). Prefer `devenv shell` (`GOTOOLCHAIN=local`).

```bash
devenv shell -- just verify    # vet + go test -race + build — gate before commit
devenv shell -- just verify-ci # same, no credentials, empty MOW_HOME
devenv shell -- just build     # → bin/mow (lean) + bin/mow-full
devenv shell -- just test      # fast inner loop (no race)
```

`just verify` matches `.github/workflows/ci.yml`. Race is part of the gate. Adding a CI step means adding it to `verify` too. **Before push: `just verify-ci`** — a developer `~/.mow` and API key make Engine tests pass locally and fail on CI. Packs that construct an Engine need `TestMain` pinning `MOW_HOME`, `MOW_API_KEY`, `MOW_MODEL` (`testutil.RunWithProvider`; see `packs/job`).

A test may not assume anything is listening on a port. Use `httptest.NewServer`. Format with `gofmt`. No Make/npm scripts.

## Request flow

`Engine.Prompt` / `PromptWith`: config → tools + hooks → `internal/agent` loop → `internal/llm` → `internal/tools` + `internal/policy` jail → session JSONL. Start at **`internal/engine/engine.go`** + **`engine_prompt.go`**, then `internal/agent/loop.go`.

System prompt: `llm.system_prefix` → optional “You are mow” if no prefix → harness rules (always) → this file / skills. See `internal/contextload/harness.go`. Events: `internal/engine/event.go`. Cancel: `Engine.Cancel()`. Tool batches: `policy.max_parallel_tools` (default 8). CoT `<think>` is stripped; committed history is tag-free.

## Layout

Source of truth: [docs/architecture.md](docs/architecture.md).

| Path | Role |
|------|------|
| `mow.go` | Public aliases: `Engine`, `Run`, events, hooks, providers |
| `internal/engine/` | Engine + behavior tests |
| `cliutil/` | CLI flags → Engine (**not** a pack) |
| `extcfg/` | Decode `extensions.<name>` |
| `testutil/` | Shared tests (pin `$MOW_HOME`) |
| `ext/` | Registration + core extensions: acp, cli, tty |
| `packs/` | Optional module `github.com/subosito/mow/packs` |
| `internal/` | Implementation — **not** an integrator import |
| `cmd/mow/` | Lean host |
| `cmd/mow-full/` | Full host |
| `ext/cli/` | Unix CLI skeleton (run, trust, doctor, …) |
| `ext/tty/` | Optional line REPL |

If integrators need something in `internal/`, re-export — do not tell them to import `internal/`.

## Packs

- Lean (`cmd/mow`): acp, cli, tty, focus, proc, cmdhook, mcp.
- Full adds goal, job, ops, review, media. Both call `ext/cli.Main`.
- Core extensions in `ext/` (root module); optional packs in `packs/` (`go.work` for local dev).
- Remove import → subcommand/tools gone. Config: `extensions.<name>` via `Engine.Extension` or `extcfg.DecodeSection`. MCP only if configured.

## Conventions

Match surrounding style; scoped diffs; no drive-by refactors. Test non-trivial logic (table-driven like nearest `*_test.go`). Prefer stdlib; no new deps without a clear need.

## Public samples (OSS)

Strangers on GitHub must not infer a private fleet, host product, or in-house gateway.

| Do | Don't |
|----|--------|
| Current public ids (`gpt-5-mini`, `gpt-5.4-mini`, `deepseek-chat`, `claude-sonnet-4`, `gemini-2.5-flash`) | Stale ids, host-only nicknames, private revs, home-fleet peer names |
| Generic roles (`peer-agent`, `api`, `gateway`, `embedders`, `host UIs`) | Private product binaries, TUI host names, ops profile names |
| “OpenAI-compatible gateway”; `http://127.0.0.1:PORT/v1`, `https://api.openai.com/v1` | Sibling monorepos as part of mow; home-lab ports, real keys, `$HOME` paths to other repos |
| `facet` / `efforts` as optional **gateway** metadata | A private catalog product as required |

Applies to `docs/`, `README.md`, `internal/config/mow.yaml.example`, CLI help, public Go comments, and fixture ids that read as examples. OK: real wire names (`openai-responses`, `anthropic-messages`) and comments that describe wire fields. Before commit on docs/examples/fixtures: no host/fleet names, no stale model generations. Gateway-prefix heuristics may stay; wording stays generic and current.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/): `type(optional-scope): short imperative subject` (no trailing period, ~72 chars). Types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `perf`. Body after a blank line: *why*, not a file list. One logical change per commit. Informal subjects (`wip`, `fix stuff`) are not acceptable.

```
feat(llm): add openai-responses wire
fix(goal): raise max_steps on resume via --max-steps
```

Non-trivial change: `devenv shell -- just verify` before commit. Docs/fixtures also follow **Public samples** above.

## Security invariants (do not regress)

- Default tools: **read, glob, grep**. Write/shell need `--allow-write` / `--allow-shell` or config.
- FS tools are path-jailed (`policy.extra_roots` / `--extra-root` expand the jail — host/CLI only; fixed at `mow.New`). **`bash` is not path-jailed**; `--allow-shell` is the trust decision. See [docs/harness.md](docs/harness.md) § Extra FS roots.
- Workspace trust is out-of-band (`$MOW_HOME/trusted`, `mow trust`) — never a marker in the repo. Project config may not set credentials, `llm.base_url`, headers, `session.dir`, power tools, or `extra_roots`.
- No secrets in logs. Config under `$MOW_HOME` (default `~/.mow`). Optional `X-Mow-*` labels (ignored by plain providers).

## Gotchas

- Always `devenv shell --` for go/just when `devenv.nix` is present.
- Engine split: `engine.go` (New), `engine_prompt.go`, `engine_model.go`, `engine_control.go`, `engine_adapt.go`, `run.go` (Options/Run).
- Tests isolate `$MOW_HOME` via `TestMain` + `github.com/subosito/mow/testutil` — not the real `~/.mow`.

## Docs map

| Doc | Read when |
|-----|-----------|
| [docs/architecture.md](docs/architecture.md) | Public/internal, LLM endpoint |
| [docs/embedding.md](docs/embedding.md) | Embed in Go: options, events, tools, hooks, sessions |
| [docs/harness.md](docs/harness.md) | Loop, tools, config, sessions |
| [docs/extensions.md](docs/extensions.md) | Packs, ACP, media, MCP |
| [docs/rpc-acp.md](docs/rpc-acp.md) | `mow acp` + extras |
| [docs/review.md](docs/review.md) | `mow-full review` / `sec`: two-pass, flags, schema, exit codes |
