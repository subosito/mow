# lsp

Opt-in language-server tools (`lsp_hover`, `lsp_definition`) and post-edit `textDocument/diagnostic`. Requires an operator-installed language server. No config means no process.

## Link

```go
import _ "github.com/subosito/mow/packs/lsp"
```

Stock `cmd/mow` blank-imports this package. The Rust `mowi` sibling project
displays its RPC-driven results.

## Commands and tools

| Surface | Name |
|---|---|
| Tools | `lsp_hover`, `lsp_definition` |

No CLI command and no slash commands. Both tools declare `ReadOnly()` so they stay available in read-only prompts. Args: `path`, `line`, `character` (0-based).

A `PostTool` hook pulls diagnostics after a successful write/edit, caps them, attaches them to the tool result, and emits `harness.lsp.diagnostics`. A slow or down server never fails a successful edit.

Paths resolve through the engine path jail when available (same boundary as `read`/`write`). RPC frames are bounded (1 MiB). `$MOW_HOME/lsp.yaml` is capped at 1 MiB.

## Config (`extensions.lsp`)

First match: `extensions.lsp` in `-config` / `$MOW_HOME/config.yaml`; if the host loaded user config, `$MOW_HOME/lsp.yaml`.

```yaml
extensions:
  lsp:
    command: gopls
    args: [serve]
    root: .
```

Keys: `command`, `args`, `root`. Empty `command` → no server.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — LSP pack
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
