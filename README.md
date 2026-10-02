# mow

**Minimal, secure-by-default agentic harness** (Go). The **library is the product API**; protocol extensions are detachable modules. External hosts can drive it over `mow acp`.

```text
Embedder / tests ──┐
ext/cli (run) ─────┼──▶  mow.Engine  ──▶  LLM HTTP (any compatible endpoint)
ext/tty · ext/acp ─┘     (core: cli · tty · acp · proc · cmdhook · mcp · goal · worktree)
```

**Why mow:** the core module has two runtime dependencies (pty, yaml) — no SDK,
no framework; any OpenAI- or Anthropic-compatible endpoint over plain HTTP;
extensions detach by removing a blank import; secure defaults (read-only tools,
workspace path jail, out-of-band project trust). OpenTelemetry remains optional
and never enters a library-only embed.

> Version is the single line in [`VERSION`](VERSION) (currently `1.0.0`).
> Tag `v$(cat VERSION)` after bumping that file. Nix and GitHub Releases read
> the same file. Highlights: [CHANGELOG.md](CHANGELOG.md).

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
- **Tools** — read/glob/grep by default; `write`, `edit`, and exact single-file
  `apply_patch` under `--allow-write`; shell remains the explicit trust cliff.
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

./bin/mow run -p "Reply with exactly: hi"         # ext/cli
./bin/mow models                                  # catalog: id, wire, efforts
./bin/mow                                         # interactive session (ext/tty)
./bin/mow tty                                     # explicit interactive session
./bin/mow acp                                     # ext/acp — ACP agent + extras
./bin/mow goal new --id fix-ci --goal "Make CI green"   # ext/goal
./bin/mow worktree add fix-auth                   # ext/worktree — parallel checkout
./bin/mow help                                # linked commands, dynamically

```

**Extension-owned subcommands:** the stock binary blank-imports linked extensions. Remove an
import (for example `_ "github.com/subosito/mow/ext/acp"`) and its tools and
subcommand disappear from that binary and help. Optional application workflows
live in separate external modules built on the public API.

Secure default tools: **read**, **glob**, **grep**. Power tools need
`--allow-write` / `--allow-shell`. `--allow-shell` is the trust cliff:
`bash` / `proc_start` are **not** path-jailed (they run as you). File
tools stay in the workspace jail. Opt-in `--sandbox` (config
`policy.sandbox: fence`) wraps both shell entry points using Fence on Linux
and macOS. Fence is required; missing required isolation is a hard error.
Outbound network is denied. This is not a VM or hostile-code containment.
Project `.mow` config/skills load only after
`mow trust` (stored out-of-band under `$MOW_HOME`, so a cloned repo cannot trust
itself), and never set credentials, endpoints, or power tools.

## Supported platforms

Linux and macOS. The engine relies on Unix process semantics
(process-group kill for `bash`/`proc_start`, advisory `flock` on session
files), and its strongest security guarantees are OS-specific: openat2
`RESOLVE_BENEATH` on Linux 5.6+, `fcntl
F_GETPATH` fd verification on macOS. Windows is intentionally unsupported
(the build fails on purpose-relevant syscalls). On kernels without openat2
the path jail falls back to check-then-verify — documented, not TOCTOU
containment.

## Modules and layout

One Go module. Full public/internal map:
[docs/architecture.md](docs/architecture.md).

| Path / module | Role |
|---|---|
| `mow.go` + `internal/` | Public API re-export and implementation |
| `ext/` + `cliutil/` + `extcfg/` | Core extensions (cli, tty, acp, proc, cmdhook, mcp, goal, workflow, worktree), CLI helpers, config decode |
| `cmd/mow/` | Stock host: `ext/cli.Main` plus linked core extensions |

## Pick extensions when embedding

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/ext/mcp" // optional MCP client + mow mcp
)
```

Import only `github.com/subosito/mow` for the Engine library. Add individual
`ext/*` imports as needed. Application extensions are maintained in
separate external modules.

Config: `extensions.<name>` (see `internal/config/mow.yaml.example`).
Docs: [docs/extensions.md](docs/extensions.md).

## Docs

| Doc | Contents |
|-----|----------|
| [AGENTS.md](AGENTS.md) | AI agents: build, spine, conventions |
| [docs/architecture.md](docs/architecture.md) | Public/internal and module boundaries |
| [docs/embedding.md](docs/embedding.md) | Embed in Go: options, events, custom tools/providers, hooks, sessions |
| [docs/harness.md](docs/harness.md) | Loop, tools, config |
| [docs/acp.md](docs/acp.md) | `mow acp` + extras |
| [docs/jev.md](docs/jev.md) | Optional structured decisions: use cases, safety, calibration, rollout |
| [docs/media.md](docs/media.md) | Binary media inputs, optional understand/generate tools, client gaps |
| [docs/orchestration.md](docs/orchestration.md) | Goal, loop, scheduler, and workflow boundaries |
| [docs/workspace-profiles.md](docs/workspace-profiles.md) | Named workspace sets: roots, overlay, skills, plugins |
| [ext/*/README.md](ext/) | Per-extension one-pagers |
| [docs/extensions.md](docs/extensions.md) | Core extensions, ACP, and MCP |

## License

MIT
