# Changelog

## Unreleased

<!-- draft: verify against final diff before release -->

- feat(decision): add an optional bounded client for native `/v1/decisions` choice/score/noul evaluation; it is not wired into loop policy.
- feat(media): preserve image/audio prompt parts through sessions and provider wires; mowi can attach jailed media with `@path` or `/attach`.
- feat(tools): add exact single-file `apply_patch`, typed tool outcomes, prompt-scoped `ask_user`, and consolidated `harness.fs.change` events.
- feat(goal): add read-only `/plan`/`mow goal plan`, persist phase/worker/verification projections, and fail when state transitions cannot be saved.
- feat(hosts): add session-local bounded `/loop` scheduling in mowi and `mow tty`; external recurrence remains a system scheduler concern.
- feat(workflow): add registered sequential prompt/verify procedures with bounded typed JSON, durable locked run state, interruption blocking, and optional CLI/slash composition.
- fix(agent): cap one tool batch at 64, ignore orphan Responses argument events, sanitize malformed resumed tool calls, and require synthesis after bounded delegate fan-out.
- fix(acp): a native delegate peer returns when the model answers. Stop-hook continuations stay on the parent. Queued calls take a cancellable peer lock and start `timeout_sec` only after they hold it. The native model check is capped at 5s and emits progress first. Identical delegate batches stop after 3 barren rounds.
- fix(llm): a Responses stream that ends on `response.done`, or an HTTP 200 JSON body, is a finished reply. A typeless error object is the provider error. A cut stream records how many frames arrived.

- feat(goal): stock `mow goal` outer loop (checklist, worktrees, parallel steps); bash refuses WIP-discard and discover-tool dumps.
- docs: OpenSpec behavior contracts for stock composition, trust, goal, and baked-in guards (`openspec/specs/`).
- refactor: drop unused leftover helpers and adopt Go 1.26 stdlib idioms (`min`/`max`, `slices`, `maps`, `errors.AsType`, `t.Context()`).
- fix(tools): jail fallback no longer removes a pre-existing outside file on verification failure; openat2 fds are CLOEXEC; read tool bounds line length; FIFOs rejected.
- fix(tools): bash and proc children no longer inherit API-key environment variables; `policy.env_passthrough: [NAMES]` (host config only) lets named variables through.
- fix(sandbox): read-only workspace is bound read-only under bwrap; bind order follows longest-match.
- fix(llm): streams ending before their terminal event, and `response.failed`, are errors instead of empty successes.
- fix(llm): anthropic and responses streams return at their final frame instead of waiting for EOF, so a gateway that holds the connection open no longer delays the turn.
- fix(agent): compaction no longer re-runs PreCompact and summary calls every turn when nothing changed.
- fix(config): trusted project config can no longer set `extensions.acp`, `llm.model`/`effort`/`stream`, `skills.selector`, or raise `policy.max_turns`; project config resolves against `--workspace`, not cwd.
- fix(contextload): the `AGENTS.md`/`CLAUDE.md` ancestor walk stops at the enclosing git root (else `$HOME`) instead of `/`; hermetic engines read the workspace only.
- fix(engine): a turn whose history did not change no longer appends a duplicate session snapshot.
- fix(acp): `mow acp` exits on SIGINT/SIGTERM while idle; concurrent prompts on one session are rejected.
- fix(mcp): concurrent tool calls on one stdio server are serialized; servers are only restarted on transport errors; refresh tokens are used before interactive login.
- fix(mcp): tool names sanitize the server name (`mcp_my-server_x` becomes `mcp_my_server_x`); duplicate names are skipped with a warning.
- fix(cli): `mow run`/`acp`/`tty` reject conflicting flags such as `--allow-shell --read-only`; `mow trust` refuses a group/other-readable trust file.
- fix(goal): `goal_process_start` requires `--allow-shell`; retry counter and wall-clock budget reset correctly; partial/blocked goals resume instead of being overwritten.
- fix(media): `generate_*` require `--allow-write`; size caps checked before reading.
- fix(review): scope content is framed as untrusted input; `--diff/--base` reject option-like values; `exit` omitted from clean JSON reports.

## 1.0.0

First stable release. ACP v1 is the host protocol (`mow acp` plus optional extras). `mow rpc` is gone.

- `extensions.acp.peers`: each row is `command` (external ACP) or `model` (native `mow acp`).
- Session **mode** (`ask`|`code`) confirms power tools; it does not strip them. `--read-only` disables write/shell and wins over yaml.
- Session **approvals** (`prompt`|`always`) skip `session/request_permission`; still gated by allow-write/shell.
- `tool_call_update` omits read/glob/grep bodies (already in history); write/edit diffs stay on the wire with `duration_ms`.
- Shared `~/.agents` discovery; drop plugin/skill folders into `$MOW_HOME/plugins`.
- `initialize` does not advertise stub `auth.logout`.
- Unreachable provider: LLM retries with backoff (~40s refused window); ACP forwards `model_retry` and returns `-32603` without the dial URL.
- Delegate peers: drop the process tree when the host Prompt ends (and on session/close); SIGKILL after 200ms if SIGTERM is ignored. Linux parent-death SIGKILL + idle reaper remain as backups.
- Native `model:` delegate peers default `permission_mode: allow` (host already capped write/shell; overlay cannot reach the TUI). External `command:` peers still default reject. Explicit yaml wins.
- Named LLM endpoints: `llm.providers` overlays; `--provider` / `MOW_PROVIDER` / `llm.provider` select one. Live `llm.*` stays the default. Native peers take `provider:` (omit = inherit host). Host yaml only.
- ACP effort options are catalog tiers only (`low`/`high`/…); the chip shows the real default_effort. No `default` pseudo-id.
- Host `EventToolEnd` keeps the full write/edit hunk (already tool-capped). Read/grep/bash still clip at 4k so a dump cannot blow the TUI.
- `delegate` is only for when the user names a peer or asks to delegate (tool description + harness rule).
- Exploration: grep-then-glob-then-read; glob is an index (junk trees skipped); unique-file reads cap at 12 this prompt.
- grep uses ripgrep when `rg` is on PATH (fixed-string, jailed, same caps); WalkDir if rg is missing, too old, or errors.
- glob uses `fd` when installed, with the same WalkDir fallback.
- bash `rg`/`grep`/`find`/`fd`/`ls`/`tree` for discovery is refused — grep tool for search, glob for listing.
- Dropped `policy.max_context_chars` and `policy.compact_ratio`. Auto-compact trips at 80% of `context_window` in tokens (same units as the ctx chip). Go `Options.MaxContextChars` remains for tests. `loop.compact` always follows `loop.compact.start`, including no-ops, so hosts can drop in-progress chrome.
- Focus pack: repeated grep/glob tool calls degrade then refuse like inventory (distinct patterns stay distinct).
- Review/sec path and whole-tree scopes use git ls-files (honour gitignore) so `.devenv` and similar trees do not burn the budget. Default excludes match glob junk dirs (`target`, `.next`, `coverage`, …).

## 1.0.0-rc.1

Prerelease of the ACP host cutover. Superseded by 1.0.0.
