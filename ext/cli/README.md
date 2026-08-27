# cli

Unix CLI skeleton for mow host binaries: dispatch, core subcommands, and free-form prompt fallback.

## Link

Import the package (not blank) and call `Main`:

```go
import cli "github.com/subosito/mow/ext/cli"

func main() {
    os.Exit(cli.Main(os.Args[1:]))
}
```

Blank-import extensions and packs separately; each registers its subcommands in `init`.

Stock `cmd/mow` links `ext/cli` plus `ext/acp`, `ext/tty`, and the lean pack set.

## Commands

| Surface | Name |
|---|---|
| CLI | `run` — one-shot prompt |
| CLI | `models` — catalog list (id, wire, efforts) |
| CLI | `trust` — workspace trust list |
| CLI | `doctor` — host/workspace inspection (no MCP) |
| CLI | `approvals` — durable tool approval rules |
| CLI | `version`, `help` |
| Slash | `doctor`, `trace`, `approvals` (for tty / host UIs) |

Core commands register with `ext.RegisterCommand` (`Layer: "ext"`) but appear under **Core** in top-level help. Other linked extensions (e.g. `tty`, `acp`) show in the **Extensions** group from `ext.Commands()`.

No-args on a TTY runs `ext.DefaultInteractiveCommand` when one is registered; otherwise usage is printed.

Free-form args (`mow fix the tests`) dispatch as `mow run -p …`. Reserved tokens (`repl`, `rpc`, pack names not linked, etc.) error instead of becoming prompts.

`run` uses `mow.NewHarness` (host path: `$MOW_HOME`, profiles, trust, sessions).

## Docs

- [docs/extensions.md](../../docs/extensions.md) — blank-import model
- [docs/harness.md](../../docs/harness.md) — engine flags and sessions
