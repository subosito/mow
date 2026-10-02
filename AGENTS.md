# AGENTS.md — working in the mow repo

mow is a **standalone** Go module: OpenAI/Anthropic-compatible HTTP. No gateway or sibling repo is required to build, test, or run. External hosts can drive `mow acp`; never import UI deps into the root module or `internal/engine`.

## Build, test, run

Requires **Go 1.26.4+** (`go.mod`). Prefer `devenv shell` (`GOTOOLCHAIN=local`).

```bash
devenv shell -- just verify    # vet + go test -race + build — gate before commit
devenv shell -- just verify-ci # same, no credentials, empty MOW_HOME
devenv shell -- just build     # → bin/mow
devenv shell -- just test      # fast inner loop (no race)
```

`just verify` matches `.github/workflows/ci.yml`. Race is part of the gate. Adding a CI step means adding it to `verify` too. **Before push: `just verify-ci`** — a developer `~/.mow` and API key make Engine tests pass locally and fail on CI. Extensions that construct an Engine need `TestMain` pinning `MOW_HOME`, `MOW_API_KEY`, and `MOW_MODEL` with `testutil.RunWithProvider`.

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
| `ext/` | Registration + core extensions: acp, cli, tty, proc, cmdhook, mcp, goal, workflow, worktree |
| `internal/` | Implementation — **not** an integrator import |
| `cmd/mow/` | Stock host |
| `ext/cli/` | Unix CLI skeleton (run, trust, doctor, …) |
| `ext/tty/` | Optional line REPL |

If integrators need something in `internal/`, re-export — do not tell them to import `internal/`.

## Extensions

- Stock `cmd/mow`: acp, cli, tty, proc, cmdhook, mcp, goal, workflow, worktree.
- Application workflows (review, job, ops, media) live in external modules built on the public API.
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
- Workspace trust is out-of-band (`$MOW_HOME/trusted`, `mow trust`) — never a marker in the repo. Project config may not set credentials, `llm.base_url`, headers, `session.dir`, power tools, `extra_roots`, or `env_passthrough`; nor `llm.model`/`effort`/`stream`, `skills.selector`, or a `max_turns` that raises the host ceiling.
- Instruction files (`AGENTS.md`/`CLAUDE.md`) are read from the workspace up to the enclosing git root (else `$HOME`) — never above; hermetic engines (`LoadUserConfig=false`) read the workspace only.
- Shell children never inherit credentials: `*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_SECRET_KEY`, `*_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, plus the configured `llm.api_key_env` / provider `api_key_env` names. Applies to `bash`, `proc_start`, the ACP terminal, command hooks, and MCP stdio servers. `policy.env_passthrough` (host/CLI only) is the exact-name opt-out and wins over the deny list.
- Children never inherit the pane binding (`HERDR_*`): stripped by the shell scrub and from ACP delegate peers, so only the top-level agent reports to the multiplexer pane.
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
| [docs/extensions.md](docs/extensions.md) | Core extensions, ACP, MCP |
| [docs/acp.md](docs/acp.md) | `mow acp` + extras |
| [docs/workspace-profiles.md](docs/workspace-profiles.md) | `$MOW_HOME/workspaces/<name>/`: roots, overlay, skills, plugins |
| [openspec/specs/](openspec/specs/) | Behavior contracts and composition decisions; in-flight work under `openspec/changes/` |
