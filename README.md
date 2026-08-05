# mow

**Minimal, secure-by-default agentic harness** (Go). The **library is the product API**; protocol extensions, workflow packs, telemetry, and the TUI are detachable modules.

```text
Embedder / tests ──┐
CLI (run/tty) ────┼──▶  mow.Engine  ──▶  LLM HTTP (any compatible endpoint)
ext + packs ──────┘     (acp · mcp · goal · review · …)
```

**Why mow:** the core module has two runtime dependencies (pty, yaml) — no SDK,
no framework; any OpenAI- or Anthropic-compatible endpoint over plain HTTP;
extensions detach by removing a blank import; secure defaults (read-only tools,
workspace path jail, out-of-band project trust). Heavy OpenTelemetry and TUI
dependencies live in nested modules and never enter a library-only embed.

> Pre-1.0: the `mow`, `ext`, and packs APIs may change between minor versions.

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

One-shot: `mow.Run(ctx, prompt, opt)`. The engine is multi-turn — call
`eng.Prompt` again and history carries.

What the embed gives you beyond a one-liner:

- **Custom transport / logging** — `Options.HTTPClient` (proxy, timeouts) and
  `Options.Logger` (capture `*slog` without touching the global default).
- **Custom LLM backend** — `Options.Provider` (streaming, tool calls, and
  token usage all preserved), or `Options.Chat` for quick fakes.
- **Per-engine tools** — `Options.Tools`; two engines in one process can run
  different toolsets.
- **Events & tokens** — `Options.OnEvent` for the lifecycle stream;
  `RunResult.Usage` / `loop.run.end` for provider-reported token totals.
- **Sessions** — `eng.Sessions()` lists resumable sessions;
  `Options.SessionID` / `Continue` resume one.

Full walkthrough with code: **[docs/embedding.md](docs/embedding.md)**.

## Build and try

```bash
devenv shell -- just verify
devenv shell -- just build    # → bin/mow
# Build the full TUI binary:
devenv shell -- bash -c 'cd packs/mowi && go build -o ../../bin/mowi ./cmd/mowi'

# Or with plain Go:
go build -o bin/mow ./cmd/mow
(cd packs/mowi && go build -o ../../bin/mowi ./cmd/mowi)

export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_API_KEY=sk-…
export OPENAI_MODEL=gpt-5-mini
# Or any OpenAI-compatible gateway:
# export OPENAI_BASE_URL=http://127.0.0.1:PORT/v1
# export OPENAI_API_KEY=…

./bin/mow run -p "Reply with exactly: hi"
./bin/mow tty
./bin/mow goal run --goal "Make CI green"   # packs/goal — multi-step
./bin/mow review                              # packs/review — advisory review
./bin/mow sec --format sarif                  # security profile / SARIF
./bin/mow job                                 # packs/job — interval jobs
./bin/mow acp                                 # ext/acp — ACP agent
./bin/mow rpc                                 # ext/rpc — JSON-lines
./bin/mow help                                # linked commands, dynamically

./bin/mowi                                    # interactive TUI
./bin/mowi trust .                            # same trust store as mow
./bin/mowi acp --model gpt-5-mini             # same pack commands as mow
./bin/mowi help
```

**Pack-owned subcommands:** stock binaries blank-import linked packs. Remove an
import (for example `_ "github.com/subosito/mow/ext/acp"`) and its tools and
subcommand disappear from that binary and help.

Secure default tools: **read**, **glob**, **grep**. Power tools need
`--allow-write` / `--allow-shell`. Project `.mow` config/skills load only after
`mow trust` (stored out-of-band under `$MOW_HOME`, so a cloned repo cannot trust
itself), and never set credentials, endpoints, or power tools.

## Modules and layout

| Path / module | Role |
|---|---|
| `mow.go` | Thin public re-export: `mow.Engine`, `mow.Run`, events, hooks, provider APIs |
| `internal/engine/` | Engine implementation and behavior tests |
| `cliutil/` | CLI flags → Engine; not a pack |
| `extcfg/` | Decode `extensions.*`; shared by extensions and packs |
| `ext/` | Registration plus core extensions: acp, mcp, proc, rpc, cmdhook, eval |
| `packs/` | Optional module: goal, review, ops, lsp, job |
| `packs/otel/` | Optional nested OpenTelemetry module |
| `packs/mowi/` | Optional nested TUI module and `cmd/mowi` full binary |
| `cmd/mow/` | CLI binary; links ext + optional packs (including OTEL), excluding TUI |
| `go.work` | Local workspace wiring all four modules |

## Pick extensions when embedding

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/ext/mcp"       // core protocol extension
    _ "github.com/subosito/mow/packs/lsp"     // optional code integration
    _ "github.com/subosito/mow/packs/ops"     // optional domain pack
)
```

Import only `github.com/subosito/mow` for the Engine library. Add individual
`ext/*` or `packs/*` imports as needed. Import
`github.com/subosito/mow/packs/otel` only when OTLP auto-wiring is wanted.

Config: `extensions.<pack>` (see `internal/config/mow.yaml.example`).
Docs: [docs/extensions.md](docs/extensions.md).

## Docs

| Doc | Contents |
|-----|----------|
| [AGENTS.md](AGENTS.md) | AI agents: build, spine, conventions |
| [docs/architecture.md](docs/architecture.md) | Public/internal and module boundaries |
| [docs/embedding.md](docs/embedding.md) | Embed in Go: options, events, custom tools/providers, hooks, sessions |
| [docs/harness.md](docs/harness.md) | Loop, tools, config |
| [docs/extensions.md](docs/extensions.md) | Core extensions, optional packs, ACP, MCP/LSP, media |

## License

MIT
