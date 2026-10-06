# cmdhook

Claude Code-style lifecycle shell hooks: a `hooks.json` declares commands per event; matching commands get the event as JSON on stdin and may return a decision on stdout. Exit code 2 blocks (stderr is the reason).

## Link

```go
import _ "github.com/subosito/mow/ext/cmdhook"
```

Stock `cmd/mow` blank-imports this package. Hooks fire in the Engine regardless
of host (CLI, TTY, or `mow acp`).

## Commands and tools

No CLI, tools, or slash commands. Hooks re-register on every `BeforeNew` (prior cmdhook hooks are cleared so profiles do not leak). There is no `mow ext` toggle — install or remove the plugin.

Host-owned Agent Plugins (`$MOW_HOME/plugins`, workspace profile `plugins/`)
that ship `hooks/hooks.json` register automatically. There is no
`extensions.cmdhook` section and no `$MOW_HOME/cmdhook.yaml`. Project
`.mow/plugins` is skills-only. Hooks run with `CLAUDE_PLUGIN_ROOT` (the plugin
folder) and `CLAUDE_PROJECT_DIR`; when the process has no `CLAUDE_CONFIG_DIR`,
hooks also receive `CLAUDE_CONFIG_DIR=$MOW_HOME` so Claude-style plugins keep
their state under mow rather than `~/.claude`.

Hooks inherit the rest of the environment, minus credentials: `MOW_API_KEY`,
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and any `*_API_KEY` are stripped before
the hook runs (same rule as the bash tool). Plugin hooks are third-party shell
snippets; they should not be handed the key mow authenticates with.

Supported hook events: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit`, `SessionStart`, `Stop`, `StopFailure`, `PreCompact`. Other events, non-`command` handler types (`http`, `prompt`, `agent`, `mcp_tool`) and Claude mod `modules` are skipped **with a message** on stderr naming them. Tool names are translated to Claude conventions for matchers (`read` → `Read`, `mcp_srv_x` → `mcp__srv_x`).

- **Decisions:** a PreToolUse `permissionDecision` of `"ask"` or `"defer"` is treated as deny. mow has no interactive prompt and cannot resume a deferred call.
- **Plain stdout** (not a JSON object) is added as model context only on `UserPromptSubmit` and `SessionStart`, as in Claude Code. On other events it is ignored (debug log), so a hook that prints cannot put text into tool results. Use `hookSpecificOutput.additionalContext` for context on tool events.
- **Stop hooks can keep the agent going:**
  - **Inputs:** they receive `last_assistant_message` and `stop_hook_active`, which is true once a Stop hook has already continued the turn.
  - **Continuing:** returning `{"decision":"block","reason":"…"}`, `hookSpecificOutput.additionalContext`, or exit 2 with a stderr reason sends that text to the model and the run continues.
  - **Cap:** at most 8 continuations in a row.
  - **Delegate peers:** a native peer (`mow acp --delegate-peer`) does not apply these continuations. The peer returns when the model answers, and the parent is the run a Stop hook can keep going.
  - **Errors:** a run that ends in an error still fires Stop, observe-only.

**Claude's contract, as implemented:**
- **Exit codes:** exit 2 blocks with stderr as the reason. On any other non-zero exit, a valid JSON object on stdout still decides; without one it is a non-blocking failure (`fail_closed` turns failures into blocks).
- **`continue: false`** stops the run after the current batch (not just this call), with `stopReason` as the message.
- **Timeouts:** defaults per event are 60 s for PreToolUse and Stop, 30 s for UserPromptSubmit, and 600 s otherwise. A plugin-wide or per-entry `timeout` overrides them. A timed-out hook gives **no decision** (logged, and policy still applies) unless the plugin is `fail_closed`.
- **Payloads:**
  - tool events carry `tool_use_id`;
  - PostToolUse and PostToolUseFailure carry `duration_ms`;
  - SessionStart reports `source: resume` when history was loaded;
  - PreCompact `block` skips the automatic compaction.
- **Caps:** `additionalContext` is capped at 10,000 characters per hook (head and tail kept). Hook stdout/stderr are capped (~64 KiB), and diagnostics redact common secrets.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — hooks, `min_turns`, `mow ext`
- [docs/harness.md](../../docs/harness.md)
- [docs/architecture.md](../../docs/architecture.md)
