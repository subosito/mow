# eval

CLI for running JSON fixture suites through the `github.com/subosito/mow/eval` harness — scripted replay (no API) or a live model.

## Link

```go
import _ "github.com/subosito/mow/ext/eval"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `eval` (`mow eval` / `mowi eval`) |

Subcommand: `run FIXTURE.json`. No tools and no slash commands.

Fixture JSON is a case, a list of cases, or `{"name","cases":[…]}`. Each case may include a `script` of assistant messages for deterministic replay; omit `script` to use the configured live model (needs a key). Fixture size is capped at 8 MiB / 500 cases.

```bash
mow eval run suite.json
mow eval run suite.json --json --timeout 10m
```

`run` flags: `--timeout` (default 10m), `--json`, plus the usual engine flags. Sessions are disabled (`NoSession`).

## Config

None. There is no `extensions.eval` section.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
