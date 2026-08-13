# job

Interval or cron jobs that invoke a saved goal or a one-shot prompt. Job depends on goal; ops uses job for `mow ops run` daemon ticks.

## Link

```go
import _ "github.com/subosito/mow/packs/job"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `job` (`mow job` / `mowi job`) |

Subcommands: `run` (alias `serve`; default if no verb), `list` (`ls`), `check` (`validate`). No tools and no slash commands.

```bash
mow job --every 10m --prompt "Summarize git status"
mow job --every 1h --goal fix-ci --allow-write --allow-shell
mow job --cron "0 9 * * 1-5" --prompt "Morning brief"
mow job run --schedules path/to/schedules.yaml
mow job list
mow job check
```

Inline flags: `--every`, `--cron`, `--prompt`, `--goal`, `--id` (default `inline`), `--schedules`. Need `--every` or `--cron`, and `--goal` or `--prompt`.

Same id never overlaps an active tick (later ticks are skipped). Each tick builds a fresh `Engine` and closes it. A `every` shorter than 1s is raised to 1s. Cron is 5-field local time. Done goals are reset via `goal.Store.Reset` before the tick; blocked goals are skipped until `mow goal run --answer`. Duplicate ids fail `check` / `run`. Disabled entries are valid.

`$MOW_HOME/job/schedules.yaml` (or `--schedules`) must be a regular file, max 1 MiB / 64 entries. An explicit `--schedules` path that is missing is an error; only the default path falls back to `extensions.job`.

## Config (`extensions.job`)

```yaml
extensions:
  job:
    schedules:
      - id: hourly
        every: 1h
        cron: ""          # 5-field; wins if both every and cron are set
        goal: fix-ci      # saved goal id; prompt is ignored if goal is set
        prompt: ""
        enabled: true     # omit = true
```

File form (`$MOW_HOME/job/schedules.yaml`) uses the same `schedules:` list.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — Job pack
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
