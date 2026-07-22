# mow documentation

**mow** is a minimal Go agentic harness: secure by default, configured with files or extended programmatically via packs. Standalone module — no other product required.

## Where to start

- **Embedding mow in a Go program?** [architecture.md](architecture.md) for the public/internal line → [embedding.md](embedding.md) for the how-to (options, events, custom tools/providers, hooks, sessions).
- **Operating the CLI?** [../README.md](../README.md) to run it → [harness.md](harness.md) for config, tools, sessions, and the token/policy knobs.
- **Writing or wiring a pack?** [extensions.md](extensions.md) — core-vs-pack boundary, CLI ownership, hooks table, ACP, media, cmdhook.
- **Contributing / an AI agent working here?** [../CONTRIBUTING.md](../CONTRIBUTING.md) and [../AGENTS.md](../AGENTS.md).

| Doc | Audience | Contents |
|-----|----------|----------|
| [../AGENTS.md](../AGENTS.md) | AI coding agents | Build/test, spine, layout, security, gotchas |
| [architecture.md](architecture.md) | Everyone | Public vs `internal/`, LLM endpoint model |
| [embedding.md](embedding.md) | Go integrators | Options, events, custom tools/providers, hooks, sessions — with code |
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
