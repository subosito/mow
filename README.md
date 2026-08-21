# mow

**Minimal, secure-by-default agentic harness** (Go). The **library is the product API**; protocol extensions and workflow packs are detachable modules. The Rust `mowi` sibling project is the TUI host over mow's RPC interface.

```text
Embedder / tests ──┐
CLI (run/tty) ────┼──▶  mow.Engine  ──▶  LLM HTTP (any compatible endpoint)
ext + packs ──────┘     (acp · mcp · goal · review · …)
```

**Why mow:** the core module has two runtime dependencies (pty, yaml) — no SDK,
no framework; any OpenAI- or Anthropic-compatible endpoint over plain HTTP;
extensions detach by removing a blank import; secure defaults (read-only tools,
workspace path jail, out-of-band project trust). OpenTelemetry remains optional
and never enters a library-only embed.

> Version is the single line in [`VERSION`](VERSION). Tag `v$(cat VERSION)`
> after bumping that file. Nix and GitHub Releases read the same file.
> RPC epoch is still `"1"` (unrelated to the tag). The `mow` / `ext` / packs
> APIs may still change until 1.0.0.

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
devenv shell -- just build    # → bin/mow (embeds VERSION)

# Nix (after first vendorHash fill):
# nix build          # → ./result/bin/mow
# nix run . -- version

# Or with plain Go:
go build -o bin/mow ./cmd/mow

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
./bin/mow sec --format sarif                  # advisory security review / SARIF
./bin/mow job                                 # packs/job — interval jobs
./bin/mow acp                                 # ext/acp — ACP agent
./bin/mow rpc                                 # ext/rpc — JSON-lines
./bin/mow help                                # linked commands, dynamically

```

**Pack-owned subcommands:** stock binaries blank-import linked packs. Remove an
import (for example `_ "github.com/subosito/mow/ext/acp"`) and its tools and
subcommand disappear from that binary and help.

Secure default tools: **read**, **glob**, **grep**. Power tools need
`--allow-write` / `--allow-shell`. `--allow-shell` is the trust cliff:
`bash` / `proc_start` are **not** path-jailed (they run as you). File
tools stay in the workspace jail. Opt-in `--sandbox=bwrap` (config
`policy.sandbox: bwrap`) wraps both shell entry points in bubblewrap on
Linux — a filesystem/home jail, not a VM; network stays on; a missing
`bwrap` is a hard error. Project `.mow` config/skills load only after
`mow trust` (stored out-of-band under `$MOW_HOME`, so a cloned repo cannot trust
itself), and never set credentials, endpoints, or power tools.

## Modules and layout

Three Go modules (`go.work` wires them for local dev). Full public/internal map:
[docs/architecture.md](docs/architecture.md).

| Path / module | Role |
|---|---|
| `mow.go` + `internal/` | Public API re-export and implementation |
| `ext/` + `cliutil/` + `extcfg/` | Core extensions, CLI helpers, extension config decode |
| `packs/` | Optional packs: goal, review, ops, job |
| `packs/otel/` | Nested OTLP module |
| `cmd/mow/` | Sole full pack host (links ext + optional packs and OTEL) |

## Pick extensions when embedding

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/packs/mcp"       // core protocol extension
    _ "github.com/subosito/mow/packs/ops"     // optional domain pack
)
```

Import only `github.com/subosito/mow` for the Engine library. Add individual
`ext/*` or `packs/*` imports as needed. Import
`github.com/subosito/mow/packs/otel` only when OTLP auto-wiring is wanted — it
is a nested module and is **not** linked into the stock `mow` binary, which is
why the root module has no OpenTelemetry dependencies.

Config: `extensions.<pack>` (see `internal/config/mow.yaml.example`).
Docs: [docs/extensions.md](docs/extensions.md).

## Docs

| Doc | Contents |
|-----|----------|
| [AGENTS.md](AGENTS.md) | AI agents: build, spine, conventions |
| [docs/architecture.md](docs/architecture.md) | Public/internal and module boundaries |
| [docs/embedding.md](docs/embedding.md) | Embed in Go: options, events, custom tools/providers, hooks, sessions |
| [docs/harness.md](docs/harness.md) | Loop, tools, config |
| [docs/review.md](docs/review.md) | Shared review workflow, `--reviewer` / `--verifier`, scope, formats, CI |
| [docs/sec.md](docs/sec.md) | Read-only security review and evidence model |
| [packs/*/README.md](packs/), [ext/*/README.md](ext/) | Per-pack / per-extension one-pagers |
| [docs/extensions.md](docs/extensions.md) | Core extensions, optional packs, ACP, MCP/LSP, media |

## License

MIT
