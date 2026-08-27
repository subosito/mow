# cmdhook

Claude Code-style lifecycle shell hooks: a `hooks.json` declares commands per event; matching commands get the event as JSON on stdin and may return a decision on stdout. Exit code 2 blocks (stderr is the reason).

## Link

```go
import _ "github.com/subosito/mow/packs/cmdhook"
```

Stock `cmd/mow` blank-imports this package. Hooks fire in the Engine regardless
of host (CLI, TTY, or `mow acp`).

## Commands and tools

No pack CLI, no tools, no slash commands. Hooks re-register on every `BeforeNew` (prior cmdhook hooks are cleared so profiles do not leak). There is no `mow ext` toggle — install or remove the plugin.

Host-owned Agent Plugins (`$MOW_HOME/plugins`, workspace profile `plugins/`)
that ship `hooks/hooks.json` register automatically. There is no
`extensions.cmdhook` section and no `$MOW_HOME/cmdhook.yaml`. Project
`.mow/plugins` is skills-only. When unset, hooks also receive
`CLAUDE_CONFIG_DIR=$MOW_HOME`.

Supported hook events: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `Stop`, `PreCompact`. Tool names are translated to Claude conventions for matchers (`read` → `Read`, `mcp_srv_x` → `mcp__srv_x`). A PreToolUse `permissionDecision` of `"ask"` is treated as deny.

Default is **fail-open** on timeout / non-2 exit (warn only). Hook stdout/stderr are capped (~64 KiB); diagnostics redact common secrets.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — hooks, `min_turns`, `mow ext`
- [docs/harness.md](../../docs/harness.md)
- [docs/architecture.md](../../docs/architecture.md)
