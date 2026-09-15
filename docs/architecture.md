# Architecture — mow

**Language:** Go  
**Public module:** `github.com/subosito/mow`

## One-liner

**mow** is a secure-by-default Go agent harness: tool loop + sessions + config,
extended by detachable protocol extensions and telemetry.
External host UIs can drive the harness over `mow acp`.

LLM access is plain OpenAI-compatible or Anthropic-compatible HTTP. No gateway,
broker, host UI, or sibling repository is required for `mow.Engine`.

## Principles

1. **Library first** — public API is `github.com/subosito/mow` (`New` / `NewHarness` / `Engine` / `Run`).
2. **Thin public root** — `mow.go` re-exports the API; implementation and behavior tests live in `internal/engine/`.
3. **Minimal core module** — no OpenTelemetry or UI dependencies enter a library-only embed.
4. **Detachable features** — core extensions live in `ext/`; application workflows can live in external modules.
5. **Extensions own commands** — `ext.RegisterCommand`; blank import controls what a binary ships.
6. **Secure by default** — read-only default tools, path jail, explicit write/shell trust.

## Modules

| Module | Path | Role |
|---|---|---|
| `github.com/subosito/mow` | root | Public API, `internal/engine`, registration, core extensions, `cmd/mow` |

External applications depend on the root public API; the root module never
depends on an application module.

## Public vs internal

### Public

| Import | Role |
|---|---|
| `github.com/subosito/mow` | Thin aliases/wrappers: `Engine`, `Run`, options, events, hooks, providers |
| `github.com/subosito/mow/ext` | Registration API: tools, lifecycle hooks, commands, `BeforeNew` |
| `github.com/subosito/mow/ext/<name>` | Core Engine surfaces: acp, cli, tty, proc, cmdhook, mcp, goal (privileged; may import internal/) |
| `github.com/subosito/mow/cliutil` | CLI flags → Engine |
| `github.com/subosito/mow/extcfg` | Decode `extensions.<name>` |

### Internal

| Package | Role |
|---|---|
| `internal/engine` | Engine implementation + behavior tests |
| `internal/agent` | Tool-calling loop, compact, steer, stall handling |
| `internal/llm` | HTTP wires, model catalog/filtering, effort/native tools |
| `internal/tools` | Built-in FS/shell tools |
| `internal/config` | YAML/env config and workspace profiles |
| `internal/policy` | Path jail and power-tool gates |
| `internal/session` | JSONL sessions and context archive |
| `internal/contextload` | Project instructions and skills |
| `internal/proc` | Shared detached process implementation |

Integrators never import `internal/*`. If an optional module needs internal
behavior, the root public package re-exports a narrow API (for example
`mow.Proc*`).

```text
Embedder / cmd/mow / external applications
                  │
                  ▼
        mow.go (public aliases/wrappers)
                  │
                  ▼
        internal/engine + internal/*
                  │
                  ▼
        LLM HTTP (compatible endpoint)
```

## Extensions

### Core extensions (`ext/`, root module)

Stock ways to *drive* the Engine. Blank-import; unused if you do not link them.
The Engine itself is headless (no argv). `Workspace` is the FS jail root, not
a profile switch.

- `ext/cli`: unix dispatcher (`cli.Main`) + `run`, `trust`, `doctor`, `approvals`, version, help. Uses `mow.NewHarness`.
- `ext/tty`: optional line REPL (`mow tty`). Does not import `ext/acp`.
- `ext/acp`: ACP agent + extras + peer delegation (`delegate`). Must not import `ext/cli`.
- `ext/proc`: detached process tools (`proc_start` / `status` / `stop`; no CLI).
- `ext/cmdhook`: command hooks from host-owned Agent Plugins.
- `ext/mcp`: MCP client tools (config-gated) + `mow mcp` stdio server.
- `ext/goal`: durable outer loop over Prompt (checklist, worktrees, parallel steps).

Application workflows (review, job, ops, media) are maintained separately in
[custom-host](https://github.com/subosito/mow).

## Binaries

| Binary | Source | Ships |
|---|---|---|
| `mow` | `cmd/mow` | Import list: `ext/cli`, `ext/tty`, `ext/acp`, `ext/proc`, `ext/cmdhook`, `ext/mcp`, `ext/goal` |

Composition lives in `cmd/` (blank imports + `ext/cli.Main`). Host UIs spawn
`mow acp`; they do not need `run` or `tty`. The Engine library requires neither.

External host UIs own presentation; extension commands and tools remain registered
in the mow host that is on `PATH`.

## Catalog behavior

`GET /v1/models` metadata may include gateway extensions (`object`, `wire`,
`wires`, `facet`, efforts, pricing, native tools, composite hops). Mow:

- filters discovery-only `object: "model_group"` entries from callable pickers;
- filters non-chat facets and unknown chat wires;
- derives a preferred wire from `wires` when aliases/composites omit `wire`;
- caches only the callable filtered catalog.

This prevents selectors from offering group ids such as `reviewers` and keeps
wire labels visible for aliases/model hops.

## Trust boundary

Project config cannot grant credentials, endpoints, power tools, session dirs,
or extra roots. Trust is stored out-of-band under `$MOW_HOME/trusted`. File tools
use the workspace/extra-root jail; shell is a separate explicit trust decision.

## See also

- [embedding.md](embedding.md) — public API and custom integrations
- [harness.md](harness.md) — loop, tools, config, sessions
- [extensions.md](extensions.md) — extension and pack details
- [workspace-profiles.md](workspace-profiles.md) — `$MOW_HOME/workspaces/<name>/`
- [openspec/specs/](../openspec/specs/) — behavior contracts (composition, trust, goal, guards)
