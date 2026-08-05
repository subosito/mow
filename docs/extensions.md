# mow — extensions and packs

**Rule:** if a capability is not required for a read-only agent over compatible
LLM HTTP, it is detachable. Core protocols/runtime adapters live under `ext/`;
workflow/domain integrations live in the separate `packs/` module; heavy OTEL
and TUI dependencies live in nested modules.

Customization modes:

1. **Configure** — YAML/env/skills; no code.
2. **Program** — `mow.Options`, custom tools/provider, `ext.Register*`.
3. **Link** — blank-import core extensions or optional packs into a binary.

## Layers and imports

| Layer | Imports | Examples |
|---|---|---|
| Public Engine | `github.com/subosito/mow` | `Engine`, `Run`, hooks, events, providers |
| Registration | `github.com/subosito/mow/ext` | `RegisterTool`, `RegisterCommand`, lifecycle hooks |
| Core extensions | `github.com/subosito/mow/ext/<name>` | acp, mcp, proc, rpc, cmdhook, eval |
| Optional packs | `github.com/subosito/mow/packs/<name>` | goal, review, ops, lsp, job |
| Heavy optional | `github.com/subosito/mow/packs/otel`, `…/packs/mowi` | OTLP and TUI |

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/ext/acp"
    _ "github.com/subosito/mow/ext/mcp"
    _ "github.com/subosito/mow/packs/goal"
    _ "github.com/subosito/mow/packs/lsp"
    _ "github.com/subosito/mow/packs/otel"
)
```

Remove an import and the associated tools/subcommand/auto-wire disappear.

## Linked binaries

- `cmd/mow` links core extensions, goal/review/ops/lsp/job, and OTEL; no TUI.
- `packs/mowi/cmd/mowi` links the full TUI and the same registered commands.
- `mowi acp`, `mowi goal`, `mowi review`, `mowi ops`, etc. dispatch through
  `ext.LookupCommand`, just like `mow`.
- `mow_agents` start the currently running executable (`os.Executable()`), so
  either binary works standalone for native ACP peers.

## Core extensions (`ext/`)

### ACP (`ext/acp`)

[Agent Client Protocol](https://agentclientprotocol.com) over JSON-RPC 2.0:

- `mow acp` / `mowi acp`: run the current host as an ACP agent.
- `acp_delegate`: delegate to named external or native peers.
- Native `mow_agents` support model, effort, system prefix, cwd, permissions,
  and timeout; peer processes are reused by name.
- ACP content supports text/media/resources and terminal methods when shell is
  allowed.

```yaml
extensions:
  acp:
    agents:
      - name: peer-agent
        command: [peer-agent, --acp]
        timeout_sec: 300
    mow_agents:
      reviewer:
        model: gpt-5-mini
        effort: high
        system_prefix: "You are a reviewer."
```

### MCP (`ext/mcp`)

Both directions:

- configured client servers contribute `mcp_<server>_<tool>` tools;
- `mow mcp` / `mowi mcp` expose `mow_prompt` as an MCP stdio server.

No config means no client process. Supports stdio and streamable HTTP plus
bearer/OAuth modes.

```yaml
extensions:
  mcp:
    mcpServers:
      fs:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
      remote:
        url: https://mcp.example/mcp
```

### Process / RPC / command hooks / eval

- `ext/proc`: `proc_start`, `proc_status`, `proc_stop` and `mow proc`.
- `ext/rpc`: JSON-lines prompt/event/cancel/status control plane.
- `ext/cmdhook`: configured lifecycle shell hooks (supports single `root` or `plugins` map, plus `min_turns`).
- `ext/eval`: eval/replay fixtures and command.

### Extension lifecycle & turn control (`mow ext` / `/ext`)

Extensions (such as MCP servers and command hook plugins) support optional `min_turns` thresholds and manual runtime activation control:

- **`min_turns: N`**: specifies turn activation threshold (default `0`, active from start). When `turn < N`, hooks/tools remain dormant.
- **`mow ext` / `/ext`**: inspect or toggle extensions at runtime:
  - `mow ext list` or `/ext list`: list registered extension instances and status.
  - `mow ext on <name>` or `/ext on <name>`: manually enable extension `<name>`, overriding `min_turns`.
  - `mow ext off <name>` or `/ext off <name>`: manually disable extension `<name>`.

```yaml
extensions:
  cmdhook:
    plugins:
      context-mode:
        root: /path/to/context-mode
        hooks_file: hooks/hooks.json
        min_turns: 5
  mcp:
    mcpServers:
      filesystem:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        min_turns: 0
```

## Optional packs (`packs/`)

### Goal (`packs/goal`)

Durable multi-step workflow around `Engine.Prompt`: checklist state, evidence,
budgets, optional parallel nodes, worktree workers, process tools, and graph
events. State lives under `$MOW_HOME/goals`.

```bash
mow goal run --goal "Make CI green"
mow goal status --id NAME
mowi goal run --goal "Make CI green"
```

### Review (`packs/review`)

Read-only two-pass code/security review (`review` and `sec` commands), with
text/JSON/JSONL/SARIF output and validated finding scope. See
[review.md](review.md).

### Ops (`packs/ops`)

Configured service profiles: services, logs, health, declared log patterns,
actions, incidents, dependencies, runbooks, and peer-assisted remediation.
No profile means no ops tools.

### LSP (`packs/lsp`)

Opt-in language-server tools (`lsp_hover`, `lsp_definition`) and post-edit
`textDocument/diagnostic`. Requires an operator-installed/configured language
server. No config means no process.

Diagnostics are sorted by severity, capped at `mow.MaxLSPDiagnostics`, attached
to successful write/edit results, and emitted as `harness.lsp.diagnostics`.

```yaml
extensions:
  lsp:
    command: gopls
    args: [serve]
    root: .
```

### Job (`packs/job`)

Interval/cron prompt or goal jobs. Job depends on goal; ops uses job for daemon
runs. Same id never overlaps an active tick.

## OpenTelemetry (`packs/otel`)

Nested module so OTEL/grpc/protobuf dependencies do not enter a library-only
embed. Blank import registers an Engine-construction hook:

```go
import _ "github.com/subosito/mow/packs/otel"
```

When `otel.endpoint` is configured, the hook attaches OTLP/HTTP tracing and
metrics; no endpoint means no exporter.

## TUI (`packs/mowi`)

Nested module so Bubble Tea/Lip Gloss/Chroma/Goldmark dependencies remain
optional. Build/install:

```bash
(cd packs/mowi && go build -o ../../bin/mowi ./cmd/mowi)
go install github.com/subosito/mow/packs/mowi/cmd/mowi@latest
```

The TUI supports sessions, streaming, model/effort pickers, tool approval,
peer streams, goal/review integration, and the same pack command surface as
`mow`.

## Configuration

Pack config is opaque under `extensions.<name>` and decoded with public helpers:

```go
var cfg Config
ok, err := extcfg.DecodeSection("name", paths, &cfg)
```

or through an Engine:

```go
err := eng.Extension("name", &cfg)
```

Project config remains subject to the core trust/security restrictions.

## Model catalog filtering

Callable selectors use the filtered catalog:

- `object: "model_group"` rows are discovery-only and excluded;
- non-chat facets and unsupported wires are excluded;
- aliases/composites that publish `wires` but omit `wire` receive a derived
  preferred chat wire, preserving selector labels and correct switching.

## Media lanes

Media stays a side lane to the chat loop:

| Tool | Endpoint / behavior |
|---|---|
| `generate_image` | image generation → workspace file |
| `generate_speech` | speech generation → workspace file |
| `generate_video` | submit/poll video generation |
| `understand_image` | chat with image parts |
| `understand_voice` | transcription endpoint |
| `understand_video` | chat with video parts |

Media tools are enabled/configured independently of extension packs.
