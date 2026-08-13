# proc

Background-process tools and CLI: start a long-lived process (dev server, watcher, mock), keep working, then inspect or stop it.

## Link

```go
import _ "github.com/subosito/mow/ext/proc"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `proc` (`mow proc` / `mowi proc`) |
| Tools | `proc_start`, `proc_status`, `proc_stop` |

CLI subcommands: `list` (alias `ls`, default), `stop <id>`, `stop-all`, `logs <id> [n]` (alias `log`).

No slash commands. Tools require `--allow-shell` (they run shell commands). `proc_start` args: `id`, `command`, optional `log`, optional `keep` (survive session exit; default false — killed on `Engine.Close`).

State lives under `$MOW_HOME/proc/<workspace-hash>/`. Stop signals the process group; log tails are size-capped.

## Config

None. There is no `extensions.proc` section.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — process / RPC / hooks / eval
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
