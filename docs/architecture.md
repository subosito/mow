# Architecture — mow

**Language:** Go  
**Module:** `github.com/subosito/mow`

---

## One-liner

**mow** is a barebone, secure-by-default Go agent harness: tool loop + sessions + config, extended with optional packs (yaml *or* programmatic registration).

**LLM access is pluggable** — OpenAI-compatible or Anthropic-compatible HTTP. Point `llm.base_url` at any provider or any compatible gateway. mow has **no hard dependency** on another product binary or monorepo sibling.

---

## Principles

1. **Library first, hosts detached** — public API is `github.com/subosito/mow` (`Engine` / `Run`). Implementation lives under **`internal/`**. Frontends and protocols are packs or separate binaries that import mow.
2. **Standalone by default** — works with a single API key + base URL. No broker, catalog service, or host UI is required to run `Engine.Prompt` or `mow run`.
3. **Minimal core, packs in `ext/`** — registration via `ext`; protocols/tools as packs (`acp`, `rpc`, `goal`, …). CLI plumbing is **`cliutil`**, not a pack.
4. **Packs own their CLI** — subcommands register with `ext.RegisterCommand`; stock binary blank-imports packs. Unlink a pack → subcommand disappears.
5. **Secure by default** — deny-by-default for shell/write; workspace-scoped FS.

---

## Public vs internal

### Public (stability / integration)

| Import | Role |
|--------|------|
| **`mow`** | `New`, `Engine`, `Run`, `Options`, `RunResult`, `Tool`/`Message`/`ChatFunc` types, `Engine.Extension` |
| **`ext`** | Registration only: `RegisterTool`, lifecycle hooks, `RegisterCommand`, `BeforeNew` |
| **`ext/<pack>`** | Optional feature packs (acp, rpc, goal, …) — blank-import to link |
| **`cliutil`** | CLI helpers (flags → `Engine`); not a pack, no registration |
| **`packcfg`** | Decode `extensions.<name>` for packs; not a pack |
| **`cmd/mow`** | Stock binary (not a library surface) |

### Internal (implementation — not a compatibility surface)

| Package | Role |
|---------|------|
| `internal/agent` | Tool-calling loop |
| `internal/llm` | OpenAI / Anthropic / media HTTP clients |
| `internal/tools` | Built-in tools + media tools |
| `internal/config` | yaml + env merge |
| `internal/policy` | Path jail, power-tool gates |
| `internal/session` | JSONL sessions |
| `internal/contextload` | AGENTS.md / CLAUDE.md, skills, trust |

Integrators should not import `internal/*`. Custom tools use `ext.Tool` / `mow.Tool` and register via `ext.RegisterTool`.

```text
  Embedder / tests     CLI (run/repl + cliutil)   ext packs (acp/rpc/…)
         │                      │                           │
         └────────────┬─────────┴───────────────────────────┘
                      ▼
            mow.Engine / mow.Run     ← public API
                      │
            internal/* (loop, llm, tools, …)
                      │
                 LLM HTTP  →  any OpenAI- or Anthropic-compatible endpoint
```

---

## Frontends and commands

| Surface | How | Owner |
|---------|-----|--------|
| **API** | `mow.New` / `Engine.Prompt` / `OnEvent` / `Cancel` / `Status` | public module |
| **run** | `mow run -p …` / `--allow-write` | **core** CLI |
| **repl** | `mow repl` | **core** CLI |
| **rpc** | `mow rpc` | **ext/rpc** JSONL control plane (prompt + event stream + cancel/status) |
| **acp** | `mow acp` | **ext/acp** (Agent Client Protocol agent) |
| **goal** | `mow goal` | **ext/goal** (outer multi-step loop) |
| **schedule** | `mow schedule serve` | **ext/schedule** (interval / cron jobs) |

### Integration matrix (hosts / orchestrators)

| Host need | Use |
|-----------|-----|
| In-process Go | `Engine.Prompt` + `Options.OnEvent` / `SetOnEvent` |
| Scripts / local tools | `mow rpc` — `prompt`, `cancel`, `status`, `event` notifications |
| Editors | `mow acp` |
| Peer harnesses | tool `acp_delegate` (session reused; chunks as `delegate.chunk` events) |
| Outer multi-step | `ext/goal` / `mow goal` (not a second core loop) |

Correlate logs and events with `run_id` (per Prompt) and `session_id`.

Stock binary (`cmd/mow`) blank-imports packs:

```go
_ "github.com/subosito/mow/ext/acp"
_ "github.com/subosito/mow/ext/goal"
_ "github.com/subosito/mow/ext/lsp"
_ "github.com/subosito/mow/ext/mcp"
_ "github.com/subosito/mow/ext/rpc"
_ "github.com/subosito/mow/ext/schedule"
```

Remove an import → that pack’s subcommand and tools (if any) leave the binary. Help lists linked packs dynamically.

---

## LLM endpoint (optional gateway)

mow only needs:

| Config | Meaning |
|--------|---------|
| `llm.base_url` | Provider or gateway `/v1` (or Anthropic root for messages wire) |
| `llm.api_key` / env | Auth for that endpoint |
| `llm.model` | Chat model id |
| `llm.wire` | `openai-chat-completions` (default) or `anthropic-messages` |

| Without a gateway | With any compatible gateway |
|-------------------|-----------------------------|
| mow → provider API | mow → `http://127.0.0.1:…/v1` (or similar) |
| Operator holds provider keys in mow env | Operator holds whatever key the gateway expects |
| One API shape per config | Gateway may route/catalog; mow still speaks plain HTTP |

Optional **attribution labels** (headers only; plain providers ignore them): `X-Mow-Actor`, `X-Mow-Session`, `X-Mow-Component`. Gateways can map these into their own observability slots. See [extensions.md](extensions.md).

---

## Trust boundary

| Process | May do | Must not |
|---------|--------|----------|
| **mow** | Read/write workspace (if allowed), shell (if allowed), call LLM HTTP | Assume another product’s broker, channel store, or UI is present |

mow never opens chat-channel sessions or external product databases. Hosts that want a TUI, desktop shell, or multi-agent dashboard **import mow** — they are not part of this module.

---

## North star

*mow is a self-contained harness: config + loop + tools + sessions. Everything else is an optional pack or an external host.*

## See also

- [harness.md](harness.md) — end-to-end design  
- [extensions.md](extensions.md) — packs, CLI ownership, ACP, media  
