# cmdhook

Claude Code-style lifecycle shell hooks: a `hooks.json` declares commands per event; matching commands get the event as JSON on stdin and may return a decision on stdout. Exit code 2 blocks (stderr is the reason).

## Link

```go
import _ "github.com/subosito/mow/ext/cmdhook"
```

Stock `cmd/mow` blank-imports this package. The Rust `mowi` sibling project
displays its RPC-driven results.

## Commands and tools

No pack CLI, no tools, no slash commands. Hooks re-register on every `BeforeNew` (prior cmdhook hooks are cleared so profiles do not leak). There is no `mow ext` toggle — use config / `min_turns`.

Supported hook events: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `Stop`, `PreCompact`. Tool names are translated to Claude conventions for matchers (`read` → `Read`, `mcp_srv_x` → `mcp__srv_x`). A PreToolUse `permissionDecision` of `"ask"` is treated as deny.

Default is **fail-open** on timeout / non-2 exit (warn only). Set `fail_closed: true` to block like exit 2. Hook stdout/stderr are capped (~64 KiB); diagnostics redact common secrets.

## Config (`extensions.cmdhook`)

`root` (single plugin) or a `plugins` map. Fallback when the host loaded user config: `$MOW_HOME/cmdhook.yaml`.

```yaml
extensions:
  cmdhook:
    fail_closed: false        # default for plugins that omit fail_closed
    root: /path/to/plugin     # ${CLAUDE_PLUGIN_ROOT}; used when plugins is empty
    hooks_file: hooks/hooks.json
    timeout_sec: 10
    min_turns: 0
    plugins:
      policy:
        root: /path/to/policy
        hooks_file: hooks/hooks.json
        timeout_sec: 10
        min_turns: 0
        fail_closed: true
```

Per-plugin keys: `name`, `root`, `hooks_file`, `timeout_sec`, `min_turns`, `fail_closed`.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — hooks, `min_turns`, `mow ext`
- [docs/harness.md](../../docs/harness.md)
- [docs/architecture.md](../../docs/architecture.md)
