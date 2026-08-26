# Mow documentation

**mow** is a minimal Go agentic harness: secure by default, configured with files or extended programmatically via packs. Standalone module — no other product required.

## Where to start

- **Embedding mow in a Go program?** [architecture.md](architecture.md) for the public/internal line → [embedding.md](embedding.md) for the how-to (options, events, custom tools/providers, hooks, sessions).
- **Operating the CLI?** [../README.md](../README.md) to run it → [harness.md](harness.md) for config, tools, sessions, and the token/policy knobs.
- **Writing or wiring a pack?** [extensions.md](extensions.md) — core-vs-pack boundary, CLI ownership, hooks table, ACP, media, cmdhook.
- **Security review or CI?** [review.md](review.md) for shared mechanics → [sec.md](sec.md) for security evidence and command boundaries.
- **Contributing / an AI agent working here?** [../CONTRIBUTING.md](../CONTRIBUTING.md) and [../AGENTS.md](../AGENTS.md).

| Doc | Audience | Contents |
|-----|----------|----------|
| [../AGENTS.md](../AGENTS.md) | AI coding agents | Build/test, spine, layout, security, gotchas |
| [architecture.md](architecture.md) | Everyone | Public vs `internal/`, LLM endpoint model |
| [embedding.md](embedding.md) | Go integrators | Options, events, custom tools/providers, hooks, sessions — with code |
| [harness.md](harness.md) | Implementers | Loop, tools, config, sessions, policy |
| [extensions.md](extensions.md) | Integrators | `ext/` packs, CLI ownership, ACP, media, decisions |
| [rpc-acp.md](rpc-acp.md) | Protocol | `mow rpc` ↔ ACP v1/v2 coverage; what stays first-party |
| [review.md](review.md) | CI + reviewers | Shared review scope, two-pass workflow, `--reviewer` / `--verifier`, schema, formats, budgets, exit codes |
| [sec.md](sec.md) | Security reviewers | `mow sec`: evidence fields, safety contract, evidence levels, future stages |
| [workspace-profiles.md](workspace-profiles.md) | CLI operators | `$MOW_HOME/workspaces/<name>/`: roots, overlay, skills, plugins, scoped ACP peers |

Per-pack / per-extension one-pagers live next to the code (`ext/*/README.md`, `packs/*/README.md`). Longer how-tos stay here.

## Dev shell

```bash
devenv shell -- just verify
devenv shell -- just build    # → bin/mow + bin/mow-full
```

## Name

**mow** — agentic harness product name. Design decisions and the full layout
table live in [architecture.md](architecture.md) and [extensions.md](extensions.md).
