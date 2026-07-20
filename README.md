# mow

**Minimal, secure-by-default agentic harness** (Go). The **library is the product API**; wire protocols and optional packs are detachable.

```text
Embedder / tests ──┐
CLI (run/repl) ────┼──▶  mow.Engine  ──▶  LLM HTTP (any compatible endpoint)
ext packs ─────────┘     (acp · rpc · goal · mcp · …)
```

**Why mow:** two runtime dependencies (pty, yaml) — no SDK, no framework; any
OpenAI- or Anthropic-compatible endpoint over plain HTTP; packs (acp, rpc,
goal, mcp, lsp, job) detach by removing a blank import; secure defaults
(read-only tools, workspace path jail, out-of-band project trust).

> Pre-1.0: the `mow` and `ext` API may change between minor versions.

## Library

```go
import "github.com/subosito/mow"

eng, err := mow.New(mow.Options{
    AllowWrite: false,
    // ConfigPaths, SessionID, Continue, …
})
res, err := eng.Prompt(ctx, "list go files")
// res.Text, res.SessionID
```

One-shot: `mow.Run(ctx, prompt, opt)`.
Custom backends: `Options.Provider` (streaming + usage preserved); per-engine
tools: `Options.Tools`; token counts: `RunResult.Usage` / run.end events.

## Try it

```bash
devenv shell -- just verify
devenv shell -- just build    # → bin/mow
# or, with plain Go (no devenv/nix needed):
go build -o bin/mow ./cmd/mow

export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_API_KEY=sk-…
export OPENAI_MODEL=gpt-4.1-mini
# Or any OpenAI-compatible gateway:
# export OPENAI_BASE_URL=http://127.0.0.1:PORT/v1
# export OPENAI_API_KEY=…

./bin/mow run -p "Reply with exactly: hi"
./bin/mow repl
./bin/mow goal run --goal "Make CI green"   # ext/goal — multi-step
./bin/mow job                    # interval jobs (goals/prompts)
./bin/mow acp                    # ext/acp — editors
./bin/mow rpc                    # ext/rpc — JSON-lines
./bin/mow help                   # lists linked packs dynamically
./bin/mow run -h                 # --long flags in help (-long also works)

# Optional: $MOW_HOME/mcp.yaml, $MOW_HOME/lsp.yaml for MCP/LSP tools
# export MOW_HOME=/tmp/mow-scratch
```

**Pack-owned subcommands:** stock `cmd/mow` blank-imports packs. Remove an import (e.g. `_ "…/ext/acp"`) and that subcommand disappears from the binary and help.

Secure default tools: **read**, **glob**, **grep**. Power tools need `--allow-write` / `--allow-shell`.
Project `.mow` config/skills load only after `mow trust` (stored out-of-band under `$MOW_HOME`, so a cloned repo cannot trust itself), and never set credentials, endpoints, or power tools.

## Layout

| Path | Role |
|------|------|
| `mow` | Public Engine API |
| `cliutil/` | CLI helpers (flags → Engine); not a pack |
| `packcfg/` | Decode `extensions.*`; not a pack |
| `ext/` | Registration + feature packs (acp, rpc, goal, mcp, lsp, …) |
| `internal/` | Loop, llm, tools, config, policy, session |
| `cmd/mow` | Thin CLI shell |

## Extensions

Blank-import packs into a custom binary, or `ext.RegisterTool` in `init`.  
Stock `cmd/mow` already links acp/rpc/goal/mcp/lsp/job.

Config: `extensions.<pack>` (see `internal/config/mow.yaml.example`).  
Docs: [docs/extensions.md](docs/extensions.md).

## Docs

| Doc | Contents |
|-----|----------|
| [AGENTS.md](AGENTS.md) | AI agents: build, spine, conventions |
| [docs/architecture.md](docs/architecture.md) | Public/internal, LLM endpoint model |
| [docs/harness.md](docs/harness.md) | Loop, tools, config |
| [docs/extensions.md](docs/extensions.md) | Packs, CLI ownership, ACP, media, decisions |

## License

MIT
