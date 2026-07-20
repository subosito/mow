# mow documentation

**mow** is a minimal Go agentic harness: secure by default, configured with files or extended programmatically via packs. Standalone module — no other product required.

| Doc | Audience | Contents |
|-----|----------|----------|
| [../AGENTS.md](../AGENTS.md) | AI coding agents | Build/test, spine, layout, security, gotchas |
| [architecture.md](architecture.md) | Everyone | Public vs `internal/`, LLM endpoint model |
| [harness.md](harness.md) | Implementers | Loop, tools, config, sessions, policy |
| [extensions.md](extensions.md) | Integrators | `ext/` packs, CLI ownership, ACP, media, decisions |

## Dev shell

```bash
devenv shell -- just verify
devenv shell -- just build    # → bin/mow
```

## Decisions (quick index)

| Decision | Doc |
|----------|-----|
| Library first; hosts/packs detachable | architecture |
| Public `mow` + `ext/*` + `cliutil`; impl under `internal/` | architecture, extensions |
| Packs own subcommands (`RegisterCommand`); CLI helpers in `cliutil` | extensions |
| ACP agent + `acp_delegate` as pack, not core | extensions |
| Media: `generate_*` / `understand_*`, filesystem I/O | extensions |
| Optional attribution labels: `X-Mow-*` | extensions |
| Goals / MCP / LSP stay packs, not core | extensions |

## Name

**mow** — agentic harness product name.
