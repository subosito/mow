# ops

Named service profiles under `$MOW_HOME/ops/<name>/`: logs, health, allowlisted argv actions, incidents, runbooks, and optional ACP peers for remediation. No profile means the tools error clearly; `mow ops run` needs a profile name.

## Link

```go
import _ "github.com/subosito/mow/packs/ops"
```

Stock `cmd/mow` blank-imports this package. The Rust `mowi` sibling project
displays its RPC-driven results. Ops uses job for daemon ticks.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `ops` (`mow ops`) |
| Tools | `ops_services`, `ops_logs`, `ops_action`, `ops_incident`, `ops_health`, `ops_log_pattern`, `ops_runbook` |

No slash commands. CLI verbs: `list` (`profiles`, `ls`), `show` (`info`), `check` (`validate`), `services`, `status`, `incidents`, `run` (`serve`, `watch`).

Profile name is always explicit (first arg after the verb, or `-p`/`--ops`, or `MOW_OPS`).

```bash
mow ops list
mow ops show prod
mow ops check prod
mow ops run prod --every 5m
mow ops run prod --once
```

`ops_action` requires `--allow-shell` (forced on for `mow ops run`). Actions are operator argv lists (no shell), 60s timeout. When `MOW_OPS` is set, the profile’s `acp.agents` are merged into `acp_delegate`.

## Config (`extensions.ops`)

Pack-level only. Profile catalogs live in `$MOW_HOME/ops/<name>/` (`config.yaml`, optional `prompt.md`, `incidents/`).

```yaml
extensions:
  ops:
    root: ""              # default $MOW_HOME/ops
    log_max_bytes: 0      # default 256 KiB when unset / 0
    log_max_lines: 0
```

Profile `config.yaml` keys (not under `extensions.ops`): `services`, `log_max_bytes`, `log_max_lines`, `model`, `wire`, `base_url`, `workspace`, `every`, `prompt`, `acp.agents`. Per service: `name`, `logs`, `actions`, `health` (`url`, `timeout`, `expected_status`, `headers`, `allowed_hosts`), `patterns` (`name`, `regex`, `threshold`, `window`, `severity`), `depends_on`, `acp`, `notes`.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — Ops pack
- [docs/architecture.md](../../docs/architecture.md)
- [docs/harness.md](../../docs/harness.md)
