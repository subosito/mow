# mcp

MCP in both directions: configured client servers become tools, and `mow mcp` exposes mow itself as an MCP stdio server.

## Link

```go
import _ "github.com/subosito/mow/ext/mcp"
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
are started automatically. Plugin server entries can include `prompt` guidance alongside the transport.
YAML / `mcp.json` of the same server name wins so a
plugin is not spawned twice. Project `.mow/plugins` is skills-only — MCP does
not auto-start from the workspace. `${CLAUDE_PLUGIN_ROOT}` is expanded and
exported to the server (the plugin folder). Unless the process or the plugin's
`env` already sets it, plugin MCP also sets `CLAUDE_CONFIG_DIR=$MOW_HOME` so
Claude-style servers keep state under mow rather than `~/.claude`.

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
        prompt: |
          Use this server for conceptual workspace searches.
          Prefer grep for exact symbols and strings.
        insecure: false
        headers: {}
        auth:
          type: bearer   # bearer | oauth | oauth2_client_credentials | oauth2_device_code | oauth2_auth_code
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

Per-server keys: `name`, `command`, `args`, `env`, `url`, `insecure`, `headers`, `auth`, `min_turns`, `timeout_sec`, `prompt`. Stdio uses `command`; streamable HTTP uses `url`. `timeout_sec` bounds one `tools/call` (default 30s). Set `-1` to wait until the turn is cancelled. A silent stdio server otherwise pins the turn. A stdio server is spawned with the harness credential scrub applied to the parent environment; its own `env` map is applied on top, so use it to hand one server a token deliberately.

### OAuth servers (`auth: {type: oauth}`)

For remote servers that follow the MCP authorization spec — they answer an
unauthenticated request with `401` and publish OAuth metadata — no client id,
endpoints, or token go in config:

```yaml
extensions:
  mcp:
    mcpServers:
      tracker:
        url: https://mcp.example/v1/mcp
        auth:
          type: oauth
          # scope: ""       # optional; default from the server's challenge
          # client_id: ""   # optional; default registers mow dynamically
```

Log in once:

```bash
mow mcp login tracker     # prints a URL; approve in the browser
mow mcp logout tracker    # forget the saved login
```

`login` discovers the authorization server (RFC 9728 resource metadata, then
RFC 8414 / OpenID metadata), registers mow as a public client (RFC 7591)
unless `client_id` is set, and runs authorization code + PKCE (S256) on a
loopback redirect, binding the token to the server with `resource`
(RFC 8707). The grant is saved to `$MOW_HOME/mcp-auth/<server>.json` (0600)
and pinned to the server URL; engines use it and refresh it silently, and
never open a browser. A server that is not logged in yet is skipped with a
`run mow mcp login <server>` hint rather than stopping mow from starting.

`prompt` is optional operator-authored guidance, appended to the system prompt
for each request while the server is active. It uses the same `min_turns` and
enabled-state checks as the server tools. Blank prompts add nothing. Replacing
a server replaces its guidance; failed setup does not leave guidance behind.
The text is additive and is not saved into conversation history. It does not
replace tool descriptions or automatically adopt server-provided instructions.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — client/server, lifecycle, `min_turns`
- [docs/harness.md](../../docs/harness.md) — MCP results as untrusted output
- [docs/architecture.md](../../docs/architecture.md)

## Exploration limits

Read-only MCP tools and recognized discovery names (such as `code_search`,
`read_file`, or `list_files`) participate in the same investigation checkpoints
as native search. Search can count as exploratory even when `readOnlyHint` is
false because it maintains an auxiliary index. This does not change tool
permissions. Mutation names such as `delete_search_index` or
`search_and_replace` are not classified as discovery merely because they
contain a search word. Opaque names without read-only metadata keep the normal
application-tool behavior; name inference is a liveness heuristic.
