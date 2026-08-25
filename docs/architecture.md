# Architecture — mow

**Language:** Go  
**Public module:** `github.com/subosito/mow`

## One-liner

**mow** is a secure-by-default Go agent harness: tool loop + sessions + config,
extended by detachable protocol extensions, workflow packs, and telemetry. The
Rust `mowi` sibling project provides the interactive TUI over `mow rpc`.

LLM access is plain OpenAI-compatible or Anthropic-compatible HTTP. No gateway,
broker, host UI, or sibling repository is required for `mow.Engine`.

## Principles

1. **Library first** — public API is `github.com/subosito/mow` (`Engine` / `Run`).
2. **Thin public root** — `mow.go` re-exports the API; implementation and behavior tests live in `internal/engine/`.
3. **Minimal core module** — no OpenTelemetry or UI dependencies enter a library-only embed.
4. **Detachable features** — core extensions live in `ext/`; optional/domain packs live in `packs/`.
5. **Packs own commands** — `ext.RegisterCommand`; blank import controls what a binary ships.
6. **Secure by default** — read-only default tools, path jail, explicit write/shell trust.

## Modules

| Module | Path | Role |
|---|---|---|
| `github.com/subosito/mow` | root | Public API, `internal/engine`, registration, core extensions, `cmd/mow`, `cmd/mow-full` |
| `github.com/subosito/mow/packs` | `packs/` | Optional packs: focus, proc, cmdhook, mcp, media, goal, review, ops, job |

`go.work` wires the two Go modules for local development. Import direction is
one-way: the packs module depends on the root public API, never the reverse.

## Public vs internal

### Public

| Import | Role |
|---|---|
| `github.com/subosito/mow` | Thin aliases/wrappers: `Engine`, `Run`, options, events, hooks, providers |
| `github.com/subosito/mow/ext` | Registration API: tools, lifecycle hooks, commands, `BeforeNew` |
| `github.com/subosito/mow/ext/<name>` | Core protocol/runtime extensions: acp, rpc |
| `github.com/subosito/mow/packs/<name>` | Optional packs: media, mcp, proc, goal, review, ops, job |
| `github.com/subosito/mow/cliutil` | CLI flags → Engine |
| `github.com/subosito/mow/extcfg` | Decode `extensions.<name>` |

### Internal

| Package | Role |
|---|---|
| `internal/engine` | Engine implementation + behavior tests |
| `internal/agent` | Tool-calling loop, compact, steer, stall handling |
| `internal/llm` | HTTP wires, model catalog/filtering, effort/native tools |
| `internal/tools` | Built-in FS/shell tools |
| `internal/config` | YAML/env config and workspace sets |
| `internal/policy` | Path jail and power-tool gates |
| `internal/session` | JSONL sessions and context archive |
| `internal/contextload` | Project instructions and skills |
| `internal/proc` | Shared detached process implementation |

Integrators never import `internal/*`. If an optional module needs internal
behavior, the root public package re-exports a narrow API (for example
`mow.Proc*`, `mow.MediaClient`).

```text
Embedder / cmd/mow / packs
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

## Extensions and packs

### Core extensions (`ext/`, root module)

- `ext/acp`: ACP agent + peer delegation (`acp_delegate`)
- `packs/mcp`: MCP server + configured MCP client tools
- `packs/proc`: detached process tools/command
- `ext/rpc`: JSON-lines control plane
- `packs/cmdhook`: configured command hooks

### Optional packs (`packs/` module)

One-pagers live next to the code (`packs/<name>/README.md`, `ext/<name>/README.md`).

- `packs/media`: generate/understand tools (config-gated)
- `packs/goal`: durable outer-loop goals
- `packs/review`: code/security review workflows
- `packs/ops`: ops profiles, logs, actions, incidents, runbooks
- `packs/job`: interval/cron jobs (depends on goal); ops depends on job

### Heavy nested modules

- Rust `mowi`: external TUI host that drives `mow rpc`

## Binaries

| Binary | Source | Ships |
|---|---|---|
| `mow` | `cmd/mow` | Lean CLI: acp, rpc, focus, proc, cmdhook, mcp |
| `mow-full` | `cmd/mow-full` | Lean set plus goal, job, ops, review, media |
| Rust `mowi` | sibling project/repository | Interactive TUI over `mow rpc` |

The Rust `mowi` host launches `mow rpc` (present in both binaries) and owns
terminal presentation; pack commands and tools remain registered in the mow
host that is on `PATH`.

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
