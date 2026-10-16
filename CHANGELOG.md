# Changelog

## Unreleased

This release slims mow down to a focused core harness, adds durable workflows, git worktrees, MCP OAuth, and image/audio input, and closes a long list of reliability and security gaps in streaming, delegation, and session storage. Read **Before you upgrade** first: a few settings and APIs changed.

### Before you upgrade

- **Application workflows moved out of mow.** The review, job, ops, and media commands and the `packs` module no longer ship with mow. The stock binary keeps the core extensions: ACP, CLI, terminal, background processes, command hooks, MCP, goals, workflows, and worktrees.
- **Fence is now the only shell sandbox.** `--sandbox` uses Fence on Linux and macOS. A config that sets `policy.sandbox: bwrap` (or `bubblewrap`) is rejected with a message pointing to `--sandbox=fence` or `policy.sandbox: fence`. Install Fence before enabling the sandbox.
- **Smaller Go API.** The media and workspace-file helpers are gone from the root package (`MediaClient`, `NewMediaClient`, `MediaClientFromConfig`, `MediaDataURL`, `MediaMIMEFromPath`, `WriteWorkspaceFile`, `ReadWorkspaceFile`), and extensions share one set of extension types.
- **Project config can do less.** A trusted project's config can no longer set ACP peers, credentials, the model, effort, streaming, or the skill selector, and can only lower limits such as `max_turns` and `compact_at_tokens`. Keep those settings in `$MOW_HOME/config.yaml`.
- **Go 1.26.9 or later** is required to build mow, for standard-library security fixes.

### New

- **Image and audio input.** Image and audio parts now travel through ACP, saved sessions, and every provider wire, and hosts can attach media from inside the workspace.
- **Structured tools.** `apply_patch` applies an exact single-file patch. `ask_user` lets the model ask the user structured questions during a prompt. Tool results carry a typed outcome, and file changes arrive as a single `harness.fs.change` event.
- **Plan before you run a goal.** `/plan` and `mow goal plan` write a durable, read-only plan before anything executes. While a goal runs, its tool calls, model text, and delegate activity stream live over ACP. Engine flags may now come before the subcommand, as in `mow goal --workspace DIR run --id ID`.
- **Workflows.** Hosts can register sequential prompt-and-verify procedures that hand bounded, typed JSON from step to step. Run state is locked and saved durably, and an interrupted run stops and waits rather than carrying on. Recurring runs are left to cron, systemd, or CI.
- **Worktrees.** The worktree extension creates isolated git worktrees and branches for parallel changes, with the same policy checks that goals use.
- **Session loops.** `/loop` repeats a prompt on an interval for as long as the interactive session stays open.
- **Interactive terminal.** Running `mow` with no arguments opens a session with multiline editing, history, completion, live progress, and cancellation. Piped input behaves as before.
- **MCP OAuth.** `auth: oauth` works with servers that use MCP-spec OAuth 2.1 with discovery and dynamic client registration. `mow mcp login` runs the browser sign-in once; the grant is stored privately and refreshed silently. A server that still needs a sign-in is skipped with a hint instead of stopping mow.
- **Stop hooks can keep the agent working.** A Stop hook that blocks (with `decision: block`, `additionalContext`, or exit code 2) sends its reason back to the model, up to 8 times in a row. Command hooks also gain `PostToolUseFailure` and `StopFailure`, honour `continue: false`, and use Claude Code's per-event default timeouts, so existing Claude Code hook plugins work as written.
- **Session scratch directory.** Each session has a private temporary directory that the file tools can reach (`Engine.ScratchDir` for extensions). When a tool result or delegate reply is too long to keep in full, the complete text is saved there and the notice names the file.
- **Recovery from context overflow.** If a provider rejects a request as too large for its context window, mow compacts the conversation to about half its size and retries the turn, up to twice per run.
- **`policy.compact_at_tokens`** (default 400k; `-1` to use the window alone) caps when auto-compaction starts, which keeps per-call cost bounded on 1M-token windows.
- **`policy.env_passthrough`** lets named environment variables through the credential scrub. It can only be set in host or CLI config.
- **Faster code search.** grep accepts several patterns in one call, and a pattern that finds nothing as plain text but looks like a regular expression is retried as one.
- **Plugins.** Symlinked plugins are discovered, hosts can see each plugin's activation state and prompt commands, and a plugin's MCP guidance applies only while it is active.
- **Workspace profile notes.** Notes kept in a workspace profile are available to the file tools when permitted, without living in the project checkout.
- **`X-Mow-Session` on every request,** so gateways can keep server-side conversation state. A header you configure still takes precedence.
- **Decision client.** The optional `decision` package evaluates choices and scores against a `/v1/decisions` endpoint. The agent loop does not use it.
- **Behavior contracts.** `openspec/specs/` documents how the engine, trust, goals, guards, and stock composition are meant to behave.

### Security

- Child processes (bash, `proc_start`, the ACP terminal, command hooks, and MCP stdio servers) no longer inherit API keys, tokens, or other secrets, nor terminal multiplexer pane variables (`HERDR_*`). Delegate peers drop the pane variables too.
- ACP filesystem requests go through the same path jail as the file tools.
- File writes stay pinned to a verified parent directory and keep the target's permissions. A failed fallback never removes a file outside the workspace, FIFOs are refused, file descriptors are not leaked to child processes, and reads are size-bounded.
- `AGENTS.md` and `CLAUDE.md` discovery stops at the enclosing git repository (or `$HOME`), and embedded engines that do not load user config read only the workspace.
- `goal_process_start` requires `--allow-shell`, and every entry point rejects contradictory flags such as `--allow-shell --read-only`.
- `mow trust` refuses a trust file that other users can read.

### Fixes

**Streaming and providers**

- A stream that ends before its final frame is reported as an error instead of an empty reply, and is retried once on every wire if nothing had been shown yet. A reply that already finished survives a dropped connection tail.
- Anthropic and Responses streams return as soon as the final frame arrives instead of waiting for the server to close the connection.
- Responses streams that end with `response.done` (as xAI sends) or with a plain HTTP 200 JSON body count as finished replies.
- Reasoning models on the Responses wire keep their encrypted reasoning between steps. It is replayed only to the model that produced it.

**ACP and delegation**

- Prompts on one session run one at a time, cancellation is reliable, and `mow acp` exits cleanly on SIGINT, SIGTERM, or end of input while idle.
- Native delegate peers return as soon as the model answers. Queued delegate calls wait on a cancellable lock and start their timeout only once they run. Unknown models fail before a peer is started, and identical delegate batches stop after three rounds without new results.
- `usage_update` reports the context used by each model call, with the run total in a separate billed field, so context meters no longer overshoot during long goals.

**Agent loop and sessions**

- Long tool results keep both their beginning and their end, and results already sent to the model are never rewritten, which keeps prompt caching effective.
- Repeated tool calls get an advisory nudge after 3 unproductive rounds and are stopped after 20, so legitimate polling and retries can still succeed.
- Compaction skips models with an unknown context window and does not run again when nothing has changed.
- Resuming a session restores its tool policy, unchanged histories are not saved again, and opening and closing a client without sending a prompt no longer leaves an empty session behind.
- A chosen reasoning effort stays in place when the model catalog refreshes or the model changes.

**Hooks, MCP, tools, and goals**

- Plain (non-JSON) hook output becomes model context only for `UserPromptSubmit` and `SessionStart`. `permissionDecision: defer` now denies the call, since mow cannot resume it later.
- Calls to one MCP stdio server are serialized, servers restart only after transport errors, refresh tokens are tried before asking for a new sign-in, and tool names from servers with dashes in their names are sanitized and de-duplicated.
- glob matches patterns that contain `/` correctly with fd 10.5.
- Stopping a background process never signals an unrelated process that reused its PID.
- Goals reset their retry and time budgets on each run, resume partial or blocked runs instead of overwriting them, and fail when their state cannot be saved.

### Performance

- Sessions save incremental checkpoints instead of rewriting the whole history every turn.
- Goal steps share an identical system prompt, so providers can cache it across the run.

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
