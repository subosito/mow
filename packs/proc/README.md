# proc

Background-process **tools**: start a long-lived process (dev server, watcher, mock), keep working, then inspect or stop it. There is no `mow proc` CLI.

## Link

```go
import _ "github.com/subosito/mow/packs/proc"
```

Stock `cmd/mow` and `cmd/mowx` blank-import this package. Host UIs over `mow acp` see the same tools; ACP extras can list running procs.

## Tools

| Surface | Name |
|---|---|
| Tools | `proc_start`, `proc_status`, `proc_stop` |

Requires `--allow-shell` (they run shell commands). `proc_start` args: `id`, `command`, optional `log`, optional `keep` (survive session exit; default false — killed on `Engine.Close`).

State lives under `$MOW_HOME/proc/<workspace-hash>/`. Stop signals the process group; log tails are size-capped. Session-scoped procs (the default) are torn down when the Engine closes.

## Config

None. There is no `extensions.proc` section.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
