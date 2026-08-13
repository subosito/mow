# mcp

MCP in both directions: configured client servers become tools, and `mow mcp` exposes mow itself as an MCP stdio server.

## Link

```go
import _ "github.com/subosito/mow/ext/mcp"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package. No config means no client process.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `mcp` (`mow mcp` / `mowi mcp`) — MCP server on stdin/stdout |
| Server tool | `mow_prompt` — prompt (required), optional `read_only` bool |
| Client tools | `mcp_<server>_<tool>` — one per tool listed by each configured server |

No slash commands. Client instances register with `min_turns` and can be toggled with `mow ext on\|off <name>`.

## Config (`extensions.mcp`)

Accepts the ecosystem `mcpServers` map and a `servers` list. First match: `extensions.mcp` in `-config` / `$MOW_HOME/config.yaml`; if the host loaded user config, `$MOW_HOME/mcp.json` then `$MOW_HOME/mcp.yaml`.

```yaml
extensions:
  mcp:
    mcpServers:
      fs:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        env: {}
        min_turns: 0
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

Per-server keys: `name`, `command`, `args`, `env`, `url`, `insecure`, `headers`, `auth`, `min_turns`. Stdio uses `command`; streamable HTTP uses `url`.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — client/server, lifecycle, `min_turns`
- [docs/harness.md](../../docs/harness.md) — MCP results as untrusted output
- [docs/architecture.md](../../docs/architecture.md)
