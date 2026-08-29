# Changelog

## 1.0.0-rc.1

ACP v1 is the host protocol (`mow acp` plus optional extras). `mow rpc` is gone.

- Lean `mow` (acp, cli, tty, focus, proc, cmdhook, mcp) vs full `mowx` (+ goal, review, ops, media).
- `extensions.acp.peers`: each row is `command` (external ACP) or `model` (native `mow acp`).
- Session **mode** (`ask`|`code`) confirms power tools; it does not strip them. `--read-only` disables write/shell and wins over yaml.
- Session **approvals** (`prompt`|`always`) skip `session/request_permission`; still gated by allow-write/shell.
- `tool_call_update` omits read/glob/grep bodies (already in history); write/edit diffs stay on the wire with `duration_ms`.
- Shared `~/.agents` discovery; plugin/skill install lives in sibling **asp**.
- `initialize` does not advertise stub `auth.logout`.
- Unreachable provider: LLM retries with backoff (~40s refused window); ACP forwards `model_retry` and returns `-32603` without the dial URL.
- Delegate peers: drop the process tree when the host Prompt ends (and on session/close); SIGKILL after 200ms if SIGTERM is ignored. Linux parent-death SIGKILL + idle reaper remain as backups.
- Exploration: grep-then-glob-then-read; glob is an index (junk trees skipped); unique-file reads cap at 12 this prompt.
- grep uses ripgrep when `rg` is on PATH (fixed-string, jailed, same caps); WalkDir if rg is missing, too old, or errors.
- glob uses `fd` when installed, with the same WalkDir fallback.
- bash `rg`/`grep`/`find`/`fd`/`ls`/`tree` for discovery is refused — grep tool for search, glob for listing.
- Dropped `policy.max_context_chars` and `policy.compact_ratio`. Auto-compact trips at 80% of `context_window` in tokens (same units as the ctx chip). Go `Options.MaxContextChars` remains for tests.
- Focus pack: repeated grep/glob tool calls degrade then refuse like inventory (distinct patterns stay distinct).
- Review/sec path and whole-tree scopes use git ls-files (honour gitignore) so `.devenv` and similar trees do not burn the budget. Default excludes match glob junk dirs (`target`, `.next`, `coverage`, …).
- TUI spawn docs: `mowi.acp_command` is an exact ACP argv, mutually exclusive with `mow_bin`.
EOF
