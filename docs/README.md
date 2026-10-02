# Mow documentation

**mow** is a minimal Go agentic harness: secure by default, configured with files or extended programmatically via packs. Standalone module — no other product required.

## Where to start

- **Embedding mow in a Go program?** [architecture.md](architecture.md) for the public/internal line → [embedding.md](embedding.md) for the how-to (options, events, custom tools/providers, hooks, sessions).
- **Operating the CLI?** [../README.md](../README.md) to run it → [harness.md](harness.md) for config, tools, sessions, and the token/policy knobs.
- **Writing or wiring an extension?** [extensions.md](extensions.md) — CLI ownership, hooks, ACP, MCP, and cmdhook.
- **Contributing / an AI agent working here?** [../CONTRIBUTING.md](../CONTRIBUTING.md) and [../AGENTS.md](../AGENTS.md).

| Doc | Audience | Contents |
|-----|----------|----------|
| [../CHANGELOG.md](../CHANGELOG.md) | Operators | 1.0.0 highlights |
| [../AGENTS.md](../AGENTS.md) | AI coding agents | Build/test, spine, layout, security, gotchas |
| [architecture.md](architecture.md) | Everyone | Public vs `internal/`, LLM endpoint model |
| [embedding.md](embedding.md) | Go integrators | Options, events, custom tools/providers, hooks, sessions — with code |
| [jev.md](jev.md) | Harness designers | Optional structured decisions: client, use cases, safety, calibration, rollout |
| [harness.md](harness.md) | Implementers | Loop, tools, config, sessions, policy |
| [extensions.md](extensions.md) | Integrators | Core extensions, CLI ownership, ACP, MCP |
| [acp.md](acp.md) | Protocol | `mow acp` + extras vs ACP v1/v2 |
| [media.md](media.md) | Hosts and operators | Binary media inputs, optional understand/generate tools, client gaps |
| [orchestration.md](orchestration.md) | Orchestrator authors | Goal, loop, scheduler, and workflow boundaries and contracts |
| [workspace-profiles.md](workspace-profiles.md) | CLI operators | `$MOW_HOME/workspaces/<name>/`: roots, overlay, skills, plugins, internal docs, scoped ACP peers |
| [../openspec/specs/](../openspec/specs/) | Humans and agents | Behavior contracts: what ships in stock mow, trust, goal, baked-in guards |

Per-extension one-pagers live next to the code (`ext/*/README.md`). Longer how-tos stay here.

## Dev shell

```bash
devenv shell -- just verify
devenv shell -- just build    # → bin/mow
```

## Name

**mow** — agentic harness product name. Design decisions and the full layout
table live in [architecture.md](architecture.md) and [extensions.md](extensions.md).
