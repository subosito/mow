# tty

Optional line-mode REPL for mow host binaries (plain terminal; not a full TUI).

## Link

Blank-import into a binary. Stock `cmd/mow` does:

```go
import _ "github.com/subosito/mow/ext/tty"
```

Drop the import and `mow tty` disappears. `ext/acp` does not depend on this package.

## Commands

| Surface | Name |
|---|---|
| CLI | `tty` (`mow tty`) — interactive line session |

In-session slash meta-commands:

- `/model [id]` — list or switch models
- `/btw <question>` — aside (not kept in context)
- `/help`, `/quit` (or `/exit`)
- Pack-registered slash commands (same registry as host UIs over `mow acp`)

Uses `mow.NewHarness` and the same engine flags as `mow run`.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — blank-import model
- [docs/acp.md](../../docs/acp.md) — host UIs over `mow acp`

