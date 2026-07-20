# mow — core vs extensions

**Rule:** if it is not required for *“read-only agent on a repo via an OpenAI-compatible (or Anthropic) API,”* it is an **extension**, a **host UI concern**, or a **later pack** — not default core.

Customization modes (see [harness.md](harness.md)):

1. **Configure** — yaml, env, skill markdown (no code).  
2. **Program** — `ext.RegisterTool` / hooks / `RegisterCommand` / custom binary / `mow.Engine`.

---

## Decisions (this doc)

| Decision | Choice | Why |
|----------|--------|-----|
| Public API | `mow` + `ext` (+ packs) | Small stability surface for embedders |
| Implementation | `internal/*` | Loop/llm/tools not a compatibility surface |
| UIs & protocols | `ext/<name>` packs | Optional; not required for `Prompt()` |
| CLI subcommands | Pack-owned via `RegisterCommand` | Unlink pack → command disappears |
| Core CLI | `run`, `repl`, `version`, `help` only | Stock thin shell |
| ACP | Pack (`ext/acp`), not core | Standard editor + peer-harness wire |
| Sub-agents | Not core loop; use ACP delegate or multi-`Engine` | Single loop in core |
| Media tools | Side-lane `generate_*` / `understand_*` | Chat model stays primary; filesystem is I/O |
| Tool naming | Verbs `generate_image`, `understand_image` | Aligns with config `llm.generate` / `llm.understand` |
| Optional attribution labels | `X-Mow-*` | Labels only; plain providers ignore them |
| Lifecycle hooks | Full agent lifecycle in core (`ext.Register*` / `mow.Hooks`) | Optimizers (context-mode-style) plug in; no product pack required |

---

## Layering

```text
┌─────────────────────────────────────────────┐
│  External host UI (desktop, channel bots, …)│  goals board, multi-agent dashboards
├─────────────────────────────────────────────┤
│  ext/* packs (blank-import into binary)     │  acp, rpc, mcp, lsp, goal, …
├─────────────────────────────────────────────┤
│  Public: mow.Engine + ext registration      │  Prompt, tools, hooks, commands
├─────────────────────────────────────────────┤
│  internal/*                                 │  agent loop, llm, tools, config, …
└─────────────────────────────────────────────┘
```

| Layer | Owns |
|-------|------|
| **Public core** | `Engine.Prompt`, secure defaults, sessions, stream, skills, media tools (when configured) |
| **ext registration** | Tools, hooks, CLI commands, BeforeNew hooks |
| **Packs** | ACP, RPC, goal, MCP, LSP, job, … |
| **Host UI** | Goals board, collab, multi-agent roster (unless promoted to a pack) |

---

## Layout: what belongs where

```text
mow/                   # Engine API (library core)
cliutil/               # CLI helpers — flags → Engine (NOT a pack)
packcfg/               # decode extensions.<name> (NOT a pack)
ext/
  ext.go               # registration API: Tool, hooks, RegisterCommand, BeforeNew
  rpc/                 # pack: JSON-lines + subcommand "rpc"
  acp/                 # pack: ACP + "acp" + acp_delegate
  goal/                # pack: outer-loop goals + "goal"
  job/                 # pack: interval jobs (`mow job`)
  mcp/                 # pack: MCP → tools
  lsp/                 # pack: LSP hover/definition (gopls, …)
cmd/mow/               # thin binary: run/repl + blank-import packs
```

| Path | Is a pack? | Role |
|------|------------|------|
| `ext/<name>` feature dirs | **Yes** | Blank-import → subcommand/tools |
| `ext` (root) | No | Registration surface for packs & integrators |
| `cliutil` | No | Shared CLI flags / help for any host binary |
| `packcfg` | No | Decode `extensions.<name>` for packs (BeforeNew) |
| `mow` | No | Core harness API |

Pack import: `github.com/subosito/mow/ext/<name>`.  
Helpers: `github.com/subosito/mow/cliutil`, `github.com/subosito/mow/packcfg`.  
Config section: `extensions.<name>` via `eng.Extension("name", &dst)` or `packcfg.DecodeSection`.

### CLI ownership (subcommand = pack)

Packs register in `init`:

```go
// e.g. ext/acp/cmd.go
func init() {
    ext.RegisterBeforeNew(RegisterFromConfig) // optional: tools before New
    ext.RegisterCommand(ext.Command{
        Name:    "acp",
        Summary: "ACP agent on stdin/stdout",
        Run:     runCmd,
    })
}
```

Stock binary only blank-imports packs:

```go
// cmd/mow/main.go
_ "github.com/subosito/mow/ext/acp"
_ "github.com/subosito/mow/ext/goal"
_ "github.com/subosito/mow/ext/lsp"
_ "github.com/subosito/mow/ext/mcp"
_ "github.com/subosito/mow/ext/rpc"
_ "github.com/subosito/mow/ext/job"
```

| Action | Effect |
|--------|--------|
| Remove `_ "…/ext/acp"` | `mow acp` gone; help line gone; `acp_delegate` not registered |
| Add a new pack + import | Subcommand appears automatically |

Core keeps: **`run`**, **`repl`**, **`version`**, **`help`**.  
Default interactive (no args + TTY): only if a linked pack sets `DefaultInteractive`.

Shared flags for any Engine CLI: `cliutil.EngineFlags` → `NewEngine()` (runs `ext.BeforeNew` first).

---

## Config: `extensions.*`

Core yaml stays agent/LLM-oriented. Pack knobs are opaque blobs:

```yaml
extensions:
  acp:
    agents:
      - name: peer
        command: [peer-agent, --acp]
        # timeout_sec: 600
  # other packs: job, mcp, lsp, …
```

- Stored as YAML nodes under `extensions` (internal config).  
- Decode: `eng.Extension("acp", &cfg)` (public; no need to import `internal/config`).  
- Example file: [`internal/config/mow.yaml.example`](../internal/config/mow.yaml.example).

### `extensions.acp`

| Field | Meaning |
|-------|---------|
| `peer_idle_sec` | Drop idle peers after N seconds (default 900; `-1` = never). Always drop if process not alive. |
| `agents[].name` | Id for `acp_delegate` arg `agent` |
| `agents[].command` | Peer argv that speaks ACP on stdio |
| `agents[].dir` | Optional cwd (default: workspace) |
| `agents[].timeout_sec` | Cap per delegated prompt (default 600) |

When agents are present, `RegisterFromConfig` (via `BeforeNew`) registers tool **`acp_delegate`**.

### `ext/goal` (outer loop)

Multi-step goals **around** `Engine.Prompt` — not a second core agent loop.

```bash
mow goal new --id fix-ci --goal "Make CI green"
mow goal run --id fix-ci --model …        # or: mow goal run --goal "…"
mow goal status -id fix-ci
mow goal list
```

State: `$MOW_HOME/goals/<id>.json` (`summary`, `last_reply`, `session_id`).  
Embed: `goal.Runner{Engine, Store}.RunSpec(ctx, spec)`; hosts may `goal.Subscribe` for progress.

Completion (any of):

- tool **`goal_report`** with `status=done` and **`summary`** (user-facing result — preferred)
- fenced **`goal-status`** JSON `{"status":"done","summary":"…"}`
- line markers `GOAL_DONE` / `GOAL_FAILED:` (summary then taken from the best assistant prose in the transcript, not the bare marker)

See result: `mow goal status --id …` or `jq -r .summary $MOW_HOME/goals/<id>.json`.

`mow goal run` uses the same compact tool progress as `run`/`repl` (`→ tool target` on stderr). On exit it prints `file: …/goals/<id>.json` and resume hints (`goal run --id`, optional `repl --session`).

| Status | Re-run |
|--------|--------|
| pending / running / failed | `mow goal run --id NAME` resumes state |
| done | `mow goal reset --id NAME` then `run --id` (or `delete` and create again) |

Also: `mow goal delete --id NAME`.

### `ext/job`

```yaml
# $MOW_HOME/job/schedules.yaml  OR  extensions.job in config
schedules:
  - id: hourly
    every: 1h
    goal: fix-ci
  - id: weekday-morning
    cron: "0 9 * * 1-5"    # min hour dom month dow (local)
    prompt: "Summarize open PRs"
```

```bash
mow job                       # daemon until Ctrl+C
mow job list                  # table of schedules + next fire
mow job check                 # validate; exit 1 if any bad
mow job --schedules path.yaml
```

Same id never overlaps a previous tick (skip if still running). Not HA — use host cron for production redundancy.

### `ext/mcp` / `ext/lsp` (supported; opt-in via config)

Both are **linked in stock `cmd/mow`**. They register tools only when configured (no config → no tools, no process spawn).

Prefer **extensions.*** in config (also `$MOW_HOME/config.yaml`); file fallbacks still work (`$MOW_HOME/mcp.yaml`, `$MOW_HOME/lsp.yaml`).

```yaml
extensions:
  mcp:
    servers:
      - name: fs
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
  lsp:
    command: gopls
    args: [serve]
    root: .
```

- MCP tools: `mcp_<server>_<tool>`
  - stdio: `command` + `args` (reconnect once on failure)
  - HTTP: `url:` Streamable HTTP POST (JSON or SSE body); optional `headers`
  - HTTP auth: `bearer`, `oauth2_client_credentials`, `oauth2_device_code` (RFC 8628), or `oauth2_auth_code` (loopback browser callback; `MOW_MCP_AUTH_CODE` for tests). 401 clears cache and retries once.
- LSP tools: `lsp_hover`, `lsp_definition` (reconnect once)

```yaml
extensions:
  mcp:
    servers:
      - name: remote
        url: http://127.0.0.1:3000/mcp
        headers:
          X-Custom: value
        auth:
          type: bearer
          token: "…"
      - name: oauth-remote
        url: https://mcp.example/mcp
        auth:
          type: oauth2_client_credentials
          token_url: https://auth.example/oauth/token
          client_id: …
          client_secret: …
          scope: mcp
```

ACP agent supports **terminal/** PTY methods when `-allow-shell` (`create` / `output` / `write` / `resize` / `wait_for_exit` / `kill` / `release`). Create returns `streaming: true`; live bytes and exit also push as `session/update` with `sessionUpdate` `terminal_output` / `terminal_exit` (clients may still poll `terminal/output`).
### Hashline edit

`tools.hashline: true` in config makes `read` emit `N:hash|line` and `edit` accept `line_hash` + `new_string`.

---

## In core (shipped)

| Capability | Why core |
|------------|----------|
| Single agent loop | Product essence |
| Session JSONL + resume | Continuity |
| Workspace path policy | Security must not be optional |
| Default-deny write/shell | Secure by default |
| `read`, `glob`, `grep` (+ opt-in write/edit/bash) | Minimal useful coding agent |
| LLM HTTP (openai + anthropic) + stream | Pluggable endpoint |
| Config yaml + env + skills | Operator ergonomics |
| AGENTS.md / CLAUDE.md load | Project instructions |
| Soft context compaction | On by default (`max_context_chars` ~100k); tool results capped (`max_tool_result_chars` ~24k) |
| Media side-lanes when configured | `generate_*` / `understand_*` |

Not core: RPC, ACP, MCP, LSP, job, goals — **packs or hosts**.

---

## Media lanes (generate / understand)

**Principle:** the agent loop is always **chat** (`llm.model`). Media is **side-lane HTTP** on separate model ids (same `base_url` / key as chat unless overridden).

| Tool | Config | HTTP shape | I/O |
|------|--------|------------|-----|
| `generate_image` | `llm.generate.image` | `POST /v1/images/generations` | → `media/image-*.png` |
| `generate_speech` | `llm.generate.speech` | `POST /v1/audio/speech` | → `media/speech-*.mp3` |
| `generate_video` | `llm.generate.video` | `POST /v1/videos/generations` + poll `GET /v1/videos/{id}` | → `media/video-*.mp4` (or job JSON) |
| `understand_image` | `llm.understand.image` | chat + image parts | path → text |
| `understand_voice` | `llm.understand.voice` | `POST /v1/audio/transcriptions` | path → text |
| `understand_video` | `llm.understand.video` | chat + video parts | path → text |

```yaml
llm:
  model: deepseek-v4-flash
  generate:
    image: grok-imagine-image-quality
  understand:
    image: qwen3-vl-plus
    voice: whisper-large-v3-turbo
tools:
  enable:
    - read
    - glob
    - grep
    - generate_image
    - understand_image
    - understand_voice
```

**Filesystem as interaction surface:** generate writes under `media/` (override `path`); understand only reads workspace paths and returns text. Enabling `generate_*` in `tools.enable` is itself the write opt-in for those tools — they write (workspace-jailed) without `--allow-write`. Chat model need not be multimodal. Tool results use stable lines `path:` / `bytes:` / `model:` for chaining.

Media models: yaml `llm.generate.*` / `llm.understand.*` (and `tools.enable`). Optional `llm.generate.speech_voice` for default TTS voice_id.

---

## ACP (`ext/acp`)

[Agent Client Protocol](https://agentclientprotocol.com) (JSON-RPC 2.0) — open standard for editors ↔ agents.

| Mode | How | Role |
|------|-----|------|
| **Agent** | `mow acp` | Editor/client → mow `Engine` |
| **Client / delegate** | tool `acp_delegate` | mow → peer harness subprocess |

Agent methods (Zed-oriented): `initialize`, `authenticate`, `logout`, `session/new|load|resume|list|delete|close`, `session/prompt`, `session/cancel`, `session/set_mode` (`ask` \| `code`), streaming `session/update` (`agent_message_chunk`, `current_mode_update`, `terminal_output` / `terminal_exit`), `session/request_permission` (auto-allow), **fs/** read/write (workspace jail), **terminal/** create|output|write|resize|wait_for_exit|kill|release when shell allowed.

**Prompt content:** text, image, audio, resource, resource_link. Media is written under `media/acp/` and referenced in the text prompt (`promptCapabilities.image|audio|embeddedContext`).

**Modes:** `ask` = read-only tools (no write/edit/bash, no terminal); `code` = full access per process policy (`--allow-write` / `--allow-shell`).

**Why ACP for delegate (not a private RPC):** same wire for editors and peer agents; avoid inventing mow↔claude, mow↔codex one-offs. Core stays **one loop**; delegation is a tool with workspace jail + timeout.

**Delegate v2:** peer process + ACP session are **reused** across `acp_delegate` calls (same agent + cwd) until idle TTL or death. Partial peer text is emitted as `EventDelegateChunk` on the parent Engine (`AddOnEvent` / `OnEvent` / rpc `event` lines). Tool result still returns the full concatenated answer.

**OnEvent fan-out:** `Engine.AddOnEvent` registers additional listeners; `SetOnEvent` replaces all. `mow rpc` uses `AddOnEvent` so a host can keep its own listener on the same Engine.

```text
Editor ──ACP──▶ mow acp ──Engine──▶ LLM
mow loop ──acp_delegate──▶ peer ACP agent (other harness)
```

### RPC control plane (`ext/rpc`)

JSONL on stdio. Methods: `prompt`, `cancel`, `status`, `session`, `version`, `ping`.  
During `prompt`, server may write notifications `{"method":"event","params":{…Event}}` (`run.start`, `token`, `reasoning`, `tool.start`, `tool.end`, `turn`, `delegate.chunk`, `run.end`). Final response includes `run_id` and `stop_reason`.

`tool.end` includes `duration_ms` (wall time for that tool). Tool batches may run up to `policy.max_parallel_tools` concurrent Exec calls (default 8); soft results append in call order. See [harness.md](harness.md) § Abort / cancel.
---

## Custom tools

Stock mow has no demo tools. In a custom binary:

```go
func init() {
    ext.RegisterTool(myTool{}) // or blank-import a pack that registers tools
}
```

### Hooks (lifecycle)

Extensions (or external adapters) register hooks — enough surface for
context-optimizer patterns (deny/rewrite tools, compress results, inject
system text) without a product-specific pack in core.

Order in `Engine.Prompt`:

```text
OnSessionStart          // once in New (system/skills already loaded)
OnUserPrompt            // each Prompt
  [OnPreCompact?]       // before each LLM call when MaxContextChars set
  LLM → OnAfterTurn
  for each tool: OnPreTool → Exec → OnPostTool
OnStop                  // after Prompt returns (success or error)
```

| Register | Can |
|----------|-----|
| `RegisterSessionStart` | Append system text for the Engine lifetime |
| `RegisterUserPrompt` | Rewrite user text; append system for this turn |
| `RegisterPreCompact` | Skip compaction or supply summary stub |
| `RegisterPreTool` | Deny, rewrite args, add context on the tool result |
| `RegisterPostTool` | Rewrite tool result the model sees |
| `RegisterAfterTurn` | Observe assistant text / tool-call turns |
| `RegisterStop` | Observe final text / error |

```go
ext.RegisterPreTool(func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
    // e.g. route large-output tools, rewrite paths, deny dangerous calls
    // Deny: true → tool result error for the model; return err → abort Prompt
    return ext.PreToolDecision{}, nil
})
ext.RegisterPostTool(func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
    // e.g. truncate / summarize large results before they hit the model
    return ext.PostToolDecision{Rewrite: true, Result: summarize(e.Result)}, nil
})
// Or pass Hooks in mow.Options / eng.AddPreTool / eng.AddPostTool
```

No `ext/contextmode` pack is required: wire any MCP or local optimizer to these hooks.

---

## Goals, MCP, sub-agents, LSP (stance)

| Feature | Core? | Recommendation |
|---------|-------|----------------|
| **Goals** | **No** | Host/UI or session events |
| **MCP** | **No** | Pack that `RegisterTool`s from servers |
| **Sub-agents** | **No** | Multi-`Engine` in host, or **`acp_delegate`** / future stricter child Engine |
| **LSP / DAP** | **No** | Tool pack or via MCP |
| **Browser / sandbox** | **No** | High risk; deploy-specific packs |

Priority for new packs: deepen ACP as needed → MCP → LSP → goals only if many UIs share one store.

---

## Optional attribution labels (`X-Mow-*`)

Optional **labels only** — not routing (routing = path + body `model`). Plain OpenAI/Anthropic endpoints ignore them. A gateway may accept these as aliases into its own attribution slots (actor / session / component).

| Header | Meaning |
|--------|---------|
| `X-Mow-Actor` | Who (`mow`, …) |
| `X-Mow-Session` | Session id |
| `X-Mow-Component` | `turn.chat`, `tool.generate_image`, … |

Constants: `internal/llm.HeaderActor` / `HeaderSession` / `HeaderComponent`.

---

## Feature menu (later packs)

mow stays minimal by default. Heavier features (hashline edit, DAP, memory, browser sandbox, …) belong as optional packs when needed — not core checklist items.

**Out of scope for mow core:** multi-provider catalog, OAuth credential brokering, channel delivery, rich host UI chrome. Hosts and gateways own those.

---

## Skills (config-only)

Markdown under `skills.dirs`, `$MOW_HOME/skills` (default `~/.mow/skills`), and trusted `workspace/.mow/skills`.  
Project config/skills require `.mow/trust` or `MOW_TRUST_PROJECT=1`.

---

## Security notes for packs

- Project-local executable extensions = full machine trust (same bar as shell).  
- Prefer compiled-in packs or explicit trust.  
- FS tools stay workspace-jailed.  
- MCP / browser / DAP: default **off**.  
- `acp_delegate` peers: timeout + cwd jail; do not inherit unrestricted shell by default.

---

## See also

- [architecture.md](architecture.md)  
- [harness.md](harness.md)  
- [agentclientprotocol.com](https://agentclientprotocol.com) — ACP  
