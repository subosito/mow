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
- `/loop INTERVAL [--max N] [--for DURATION] [--ephemeral] PROMPT`; `/loop status|now|stop` — session-local recurring prompts (minimum 1m; not restored)
- `/help`, `/quit` (or `/exit`)
- Pack-registered slash commands (same registry as host UIs over `mow acp`)

Uses `mow.NewHarness` and the same engine flags as `mow run`.

## Terminal behavior

- Enter submits; blank input does nothing. Ctrl+J inserts a newline.
- Bracketed paste stays in the editor until explicitly submitted, including
  multiline text. Arrow keys edit and recall input; Ctrl+R searches history.
- Tab completes built-in and registered slash commands.
- Ctrl+C clears input or cancels the active turn. Ctrl+D on an empty prompt
  exits. SIGTERM cancels work and exits with status 143, restoring the terminal.
- Streaming is on by default in an interactive terminal. Use `--stream=false`
  to print only the completed response. Streamed answers are not printed twice.
- Normal output shows brief progress and a turn summary; `--verbose` includes
  tool targets. Responses retain their Markdown text and normal scrollback.
- Input history is in-memory only; tty does not create a second persistent
  prompt log. Engine session persistence still follows the normal session flags.
- `NO_COLOR` disables tty output styling. Redirected streams and `TERM=dumb` use plain
  line input and output, with no cursor controls or prompts. Status goes to
  stderr and answers to stdout. Oversized input reports an error.

The editor uses [readline](https://github.com/reeflective/readline); there is no
alternate screen, mouse interface, or dependency on an external TUI.

This replaces `mow tty` and `ext/tty`; no compatibility alias is registered.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — blank-import model
- [docs/acp.md](../../docs/acp.md) — host UIs over `mow acp`
