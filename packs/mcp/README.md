# mcp

MCP in both directions: configured client servers become tools, and `mow mcp` exposes mow itself as an MCP stdio server.

## Link

```go
import _ "github.com/subosito/mow/packs/mcp"
```

Stock `cmd/mow` blank-imports this package. The TUI talks to `mow acp`, not
this server. No config means no client process.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `mcp` (`mow mcp`) — MCP server on stdin/stdout |
| Server tool | `mow_prompt` — prompt (required), optional `read_only` bool |
| Client tools | `mcp_<server>_<tool>` — one per tool listed by each configured server |

No slash commands. Client instances register with `min_turns` (dormant until that turn). There is no `mow ext on|off` — change config to enable or disable a server.

Host-owned Agent Plugins (`$MOW_HOME/plugins`, workspace profile `plugins/`)
that declare `mcpServers` in `plugin.json` (or `.claude-plugin/plugin.json`)
are started automatically. YAML / `mcp.json` of the same server name wins so a
plugin is not spawned twice. Project `.mow/plugins` is skills-only — MCP does
not auto-start from the workspace. `${CLAUDE_PLUGIN_ROOT}` is expanded.
When unset, plugin MCP also sets `CLAUDE_CONFIG_DIR` to `$MOW_HOME`.

## Config (`extensions.mcp`)

Accepts the ecosystem `mcpServers` map and a `servers` list. First match:
`extensions.mcp` in `-config` / `$MOW_HOME/config.yaml` / trusted
`.mow/config.yaml`. If that section is empty: `$MOW_HOME/mcp.json`, then
`$MOW_HOME/mcp.yaml`. If those are empty too and the workspace is trusted:
`<workspace>/mcp.json` (repo-root standard file). `.mow/mcp.json` is not
read — keep project MCP in `extensions.mcp` or the root file.

```yaml
extensions:
  mcp:
    mcpServers:
      fs:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        env: {}
        min_turns: 0
        # timeout_sec: 30  # tools/call bound; omit = 30s; -1 = wait until turn cancel
      remote:
        url: https://mcp.example/mcp
        insecure: false
        headers: {}
        auth:
          type: bearer   # bearer | oauth2_client_credentials | oauth2_device_code | oauth2_auth_code
          token: ""
          token_url: ""
          device_auth_url: ""
          authorize_url: ""
          redirect_uri: ""
          client_id: ""
          client_secret: ""
          scope: ""
          header: Authorization
          prefix: "Bearer "
    # servers:               # list form; each entry carries its own name
    #   - name: fs
    #     command: npx
```

Per-server keys: `name`, `command`, `args`, `env`, `url`, `insecure`, `headers`, `auth`, `min_turns`, `timeout_sec`. Stdio uses `command`; streamable HTTP uses `url`. `timeout_sec` bounds one `tools/call` (default 30s). Set `-1` to wait until the turn is cancelled. A silent stdio server otherwise pins the turn.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — client/server, lifecycle, `min_turns`
- [docs/harness.md](../../docs/harness.md) — MCP results as untrusted output
- [docs/architecture.md](../../docs/architecture.md)
