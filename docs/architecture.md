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
| `github.com/subosito/mow` | root | Public API, `internal/engine`, registration, core extensions, `cmd/mow` |
| `github.com/subosito/mow/packs` | `packs/` | Optional workflow/domain packs: goal, review, ops, lsp, job |
| `github.com/subosito/mow/packs/otel` | `packs/otel/` | Optional OTLP auto-wire/export module |

`go.work` wires the three Go modules for local development. Import direction is
one-way: nested modules depend on the root public API, never the reverse.

## Public vs internal

### Public

| Import | Role |
|---|---|
| `github.com/subosito/mow` | Thin aliases/wrappers: `Engine`, `Run`, options, events, hooks, providers |
| `github.com/subosito/mow/ext` | Registration API: tools, lifecycle hooks, commands, `BeforeNew` |
| `github.com/subosito/mow/ext/<name>` | Core protocol/runtime extensions: acp, mcp, proc, rpc, cmdhook, eval |
| `github.com/subosito/mow/packs/<name>` | Optional packs: goal, review, ops, lsp, job, contextsink |
| `github.com/subosito/mow/packs/otel` | Optional OTLP integration |
| `github.com/subosito/mow/cliutil` | CLI flags → Engine |
| `github.com/subosito/mow/extcfg` | Decode `extensions.<name>` |

### Internal

| Package | Role |
|---|---|
| `internal/engine` | Engine implementation + behavior tests |
| `internal/agent` | Tool-calling loop, compact, steer, stall handling |
| `internal/llm` | HTTP wires, model catalog/filtering, effort/native tools |
| `internal/tools` | Built-in and media tools |
| `internal/config` | YAML/env config and workspace sets |
| `internal/policy` | Path jail and power-tool gates |
| `internal/session` | JSONL sessions and context archive |
| `internal/contextload` | Project instructions and skills |
| `internal/proc` | Shared detached process implementation |

Integrators never import `internal/*`. If an optional module needs internal
behavior, the root public package re-exports a narrow API (for example
`mow.Proc*`).

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
- `ext/mcp`: MCP server + configured MCP client tools
- `ext/proc`: detached process tools/command
- `ext/rpc`: JSON-lines control plane
- `ext/cmdhook`: configured command hooks
- `ext/eval`: eval/replay command (optional; not linked in stock `cmd/mow`)

### Optional packs (`packs/` module)

One-pagers live next to the code (`packs/<name>/README.md`, `ext/<name>/README.md`).

- `packs/goal`: durable outer-loop goals
- `packs/review`: code/security review workflows
- `packs/ops`: ops profiles, logs, actions, incidents, runbooks
- `packs/job`: interval/cron jobs (depends on goal); ops depends on job
- `packs/contextsink`: per-session store + stub for oversized tool results
  (recovery via the core `recall` tool)

### Heavy nested modules

- `packs/otel`: OpenTelemetry dependencies stay isolated unless imported
- Rust `mowi`: external TUI host that drives `mow rpc`

## Binaries

| Binary | Source | Ships |
|---|---|---|
| `mow` | `cmd/mow` | Full CLI host: core extensions + optional packs + OTEL |
| Rust `mowi` | sibling project/repository | Interactive TUI over `mow rpc` |

The Rust `mowi` host launches `mow rpc` and owns terminal presentation; pack
commands and tools remain registered in the `mow` host.

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
