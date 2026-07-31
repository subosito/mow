# mow harness — end-to-end design

**Binary:** `mow`  
**Module:** `github.com/subosito/mow`  

Stack context: [architecture.md](architecture.md). Embedding in Go: [embedding.md](embedding.md). Packs: [extensions.md](extensions.md).

---

## 1. Product definition

### What mow is

**mow** = compiled Go **agentic harness**:

- Runs a **tool-calling agent loop** (model proposes tools → host executes → results back → until done or limit).
- Talks to an **LLM HTTP endpoint** (OpenAI Chat Completions and/or Anthropic Messages).
- Is configured by **files + env**, or **embedded** via `mow.Engine`.
- Is **secure by default**: restricted tools and workspace until the operator opts into power.

### What mow is not

- Not a multi-provider model gateway or credential broker (point at one HTTP endpoint).
- Not a chat-channel product (hosts that need channels import mow or call its API).
- Not a full IDE agent (goals/MCP/LSP live as packs or external hosts).
- Not ACP by default in the core library — ACP is the **`ext/acp` pack**.

---

## 2. End-to-end flows

### A. One-shot (CI / scripts)

```text
mow run -p "Summarize this repo"
  → load config + AGENTS.md + workspace
  → agent loop
  → print answer → exit
```

### B. Interactive / multi-step

```text
mow tty                  # line session (core)
mow goal run --goal "…"  # multi-step outer loop (or --id NAME)
mow run -p "…"           # one-shot
```

### C. Programmatic embed

```go
eng, err := mow.New(mow.Options{
    ConfigPaths: []string{"/path/to/config.yaml"},
    // AllowWrite, SessionID, Continue, Chat (tests), …
})
res, err := eng.Prompt(ctx, "Add a test for X")
// multi-turn: eng.Prompt(ctx, "…") again
```

### D. Optional gateway path

```yaml
llm:
  base_url: http://127.0.0.1:PORT/v1   # any OpenAI-compatible gateway
  api_key_env: OPENAI_API_KEY         # gateway key
  model: gpt-5-mini                   # model id the gateway routes
```

Same binary; only config changes. Gateway is never required.

### E. ACP (pack)

```text
Editor ──stdio ACP──▶ mow acp ──▶ Engine
mow loop ──tool acp_delegate──▶ peer ACP process
```

---

## 3. Package inventory

### Public

| Package | Responsibility |
|---------|----------------|
| `mow` | `New`, `Engine`, `Run`, options/result types, `Tool`/`Message`/`ChatFunc`, `Engine.Extension` (files: `engine_*.go`, `run.go`) |
| `ext` | Register tools, hooks, **CLI commands**, BeforeNew (registration only) |
| `cliutil` | Shared CLI flags / `--long` help / `NewEngine` — **not** a pack |
| `packcfg` | Decode `extensions.<name>` — **not** a pack |
| `ext/rpc` | JSON-lines embed protocol + `mow rpc` |
| `ext/acp` | ACP agent + client + `acp_delegate` + `mow acp` |
| `ext/goal` | Outer multi-step goals + `mow goal` |
| `ext/job` | Interval / cron jobs + `mow job` |
| `ext/mcp` | MCP servers → tools (config opt-in) |
| `ext/lsp` | `lsp_hover` / `lsp_definition` via gopls etc. (config opt-in) |
| `cmd/mow` | Thin shell: core commands + blank-import packs |

### Internal

| Package | Responsibility |
|---------|----------------|
| `internal/agent` | Loop: messages, tool calls, max turns, abort, compaction |
| `internal/llm` | OpenAI + Anthropic chat; media HTTP (generate/understand) |
| `internal/tools` | Built-in FS/shell + media tools |
| `internal/config` | yaml + env; `extensions` blobs |
| `internal/policy` | Workspace jail, power-tool gates |
| `internal/session` | JSONL persistence, resume |
| `internal/contextload` | AGENTS.md / CLAUDE.md, skills, project trust |

Do **not** import `internal/*` from outside the module’s own packages.

---

## 4. Agent loop

```text
state = [system, …history, user]
for turn in 1..max_turns:
  resp = llm.Chat(state, tools=enabled_schemas)
  if resp has tool_calls:
    run tool batch (up to max_parallel_tools concurrent):
      if policy denies → soft tool error result
      else result = tool.Exec(call)   # PreTool → Exec → PostTool
    append soft results in call order
    on hard error / ctx cancel → fail-fast (cancel siblings), stop run
    continue
  else:
    append assistant text
    return final text
```

**Limits:** `max_turns`, bash timeout, max read bytes, **tool result cap**, **parallel tools**, soft history compaction (on by default).

### Abort / cancel

| Source | Behavior |
|--------|----------|
| `context` cancel (`Engine.Cancel`, Ctrl+C, ACP `session/cancel`) | Hard-abort the run |
| Mid-batch | Remaining / sibling tools are cancelled (fail-fast); finished soft results still append in order |
| Soft tool errors | Model-visible `"error: …"` string; batch continues |
| Child-only timeout (e.g. bash 300s) | Soft error if parent ctx still alive |
| `mow tty` Ctrl+C | Cancels **current turn only**; session stays up for the next prompt |
| `mow run` Ctrl+C | Exit code 130 |

Lifecycle slog (`mow run/tool start|end`) is **Debug** by default. Stock CLI prints compact progress on stderr (`→ read path`, `→ grep pattern`) via `OnEvent`; use `--verbose` for Debug logs.

### Token efficiency (defaults)

| Knob | Default | Effect |
|------|---------|--------|
| `policy.max_turns` | `120` | Optional budget: LLM round-trips per Prompt (a turn may batch up to 8 tools). Default 120 for casual use. `0` / CLI `--max-turns 0` / yaml `-1` = **no turn limit** (hours/days OK; stop with Ctrl+C). Only enforced when > 0 — no hidden safety cap on unlimited |
| `policy.max_context_chars` | `100000` | Soft history budget (chars). When still on this default, auto-scales from `GET /v1/models` `context_window` × `compact_ratio`. Explicit positive value = absolute budget. Set `-1` to disable. Compaction pins user task anchors + tools used so the goal is not lost |
| `policy.compact_ratio` | `0.8` | Fraction of gateway `context_window` used as history budget when auto-scaling (1M → ~800k tok-eq ≈ 3.2M chars). Clamped to 0.3–0.95. Ignored when `max_context_chars` is an explicit non-default absolute |
| `llm.context_window` / `input_price` / `output_price` | (optional) | Override metering; otherwise `Engine.Limits()` uses `GET /v1/models` fields (`context_window`, `pricing.input_per_mtok` / `output_per_mtok`) — no client-side price table |
| `policy.max_tool_result_chars` | `24000` | Cap each tool result stored for the model (~6k tokens) |
| `policy.max_read_bytes` | `512KiB` | Cap `read` tool raw file size |
| `policy.bash_timeout_sec` | `300` | Per bash call. A coding agent runs builds and test suites, so the default is minutes; a single call may request longer via the tool's `timeout_sec` argument |
| `policy.max_bash_timeout_sec` | `900` | Ceiling on a per-call `timeout_sec` request, so a hung command cannot park the loop |
| `policy.max_parallel_tools` | `8` | Concurrent tool Exec per assistant batch; `1` = sequential |
| Loop truncate | always | Oversized tools trimmed even under context budget |

Compaction is **character-estimate**, not a real tokenizer. It keeps the system message + **task anchors** (substantive user intents) + recent turns (aligned on a user boundary), stubs the middle (including which tools ran in the dropped span), and shrinks older tool bodies first.

**PreCompact hooks** (`RegisterPreCompact` / `Options.OnPreCompact`) already run when history exceeds the budget: skip compaction, or supply a better `Summary` (event includes `Messages` on the public API). Use that for LLM distill if the default stub is not enough; anchors still pin user intents either way.

**Hooks concurrency:** when `max_parallel_tools > 1`, PreTool/PostTool may run on multiple tools at once. Keep hooks non-blocking and concurrency-safe (the built-in event emitter is).

---

## 5. LLM client

| Config | Meaning |
|--------|---------|
| `llm.base_url` | Provider or gateway `/v1` |
| `llm.api_key` / `api_key_env` | Provider key **or** gateway key |
| `llm.model` | Model id |
| `llm.effort` | Reasoning intensity: `none` \| `low` \| `medium` \| `high` (optional body fields; never part of model id) |
| `llm.wire` | `openai-chat-completions` (default) \| `openai-responses` \| `anthropic-messages`. If unset, Engine aligns to the **catalog preferred wire** for `llm.model` after `GET /v1/models` (e.g. `claude-sonnet-4` → `anthropic-messages`). Explicit `llm.wire` / `MOW_WIRE` is never overridden |
| `llm.headers` | Optional extra headers |
| `llm.stream` | SSE content deltas (both wires) |
| `llm.prompt_cache` | Provider prompt caching (default on). **Only effective on `anthropic-messages`**: mow sends `cache_control` on system / tools / last message so Anthropic bills repeated prefixes as cache reads (~90% cheaper input). On `openai-chat-completions` mow does not emit Anthropic cache breakpoints — a gateway **O2A** translator (`adapter:wire-translate-o2a`) typically shows `cache_read_tokens=0` and full input rates. Prefer native `anthropic-messages` for Claude. Set `false` only if a gateway rejects `cache_control` |
| `llm.system_prefix` | Optional text segments prepended **before** the compiled system (each list item separate). For product identity / provider preambles. User config only — not project `.mow/config` |
| `llm.system_prefix_models` | Case-insensitive globs limiting when `system_prefix` applies. **Empty = every model** when prefix is set. Re-evaluated each call (follows `SetModel`) |

**System prompt composition** (request order):

1. `llm.system_prefix` segments (optional) — product identity / provider preambles (“You are …”)
2. **Identity line** — only when **no** system_prefix applies to the active model: `You are mow, …`
3. **Harness operating rules** (always) — workspace-agnostic; no second identity
4. Project `AGENTS.md` / `CLAUDE.md` (walk) + optional `$MOW_HOME/AGENTS.md`
5. Skills + per-call `SystemAppend` (goal protocol, packs)

When prefix matches the model (e.g. Claude family → “You are Claude Code”), the “You are mow” line is **omitted** so the model does not see two identities. Prefix sets persona; harness rules still apply.

| `llm.generate.*` | Side-lane model ids for generate tools |
| `llm.understand.*` | Side-lane model ids for understand tools |

**Wire vs cost (Claude / Anthropic):** `--model claude-…` without an explicit wire used to stay on `openai-chat-completions`. Gateways often accept that and translate (O2A), but **prompt cache is not applied** on that path, so large sessions (100k+ input tokens every call) cost far more than native Anthropic with cache hits. Catalog auto-align (and `/model` via `SetModelWithWire`) select `anthropic-messages` when the catalog advertises it.

No provider OAuth in mow. Streaming: `OnToken` / `OnEvent` / ACP `session/update` chunks.

**Embedder overrides** (code, not config): `Options.HTTPClient` routes all
LLM/media HTTP through your transport (proxy, timeouts, middleware);
`Options.Logger` captures engine logs on your own `*slog.Logger`;
`Options.Provider` swaps the backend entirely. See [embedding.md](embedding.md).

Optional HTTP attribution labels: `X-Mow-Actor`, `X-Mow-Session`, `X-Mow-Component` (see [extensions.md](extensions.md)).

---

## 6. Built-in tools

| Tool | Default? | Notes |
|------|----------|--------|
| `read`, `glob`, `grep` | **Yes** | Secure defaults |
| `write`, `edit` | **No** | `--allow-write` or config |
| `bash` | **No** | `--allow-shell` or config |
| `generate_*` / `understand_*` | **No** | Model ids + explicit names in `tools.enable`; generate writes under `media/` without `--allow-write` (enable list is the opt-in) |

```yaml
tools:
  enable:
    - read
    - glob
    - grep
    - generate_image   # needs llm.generate.image
    - understand_image # needs llm.understand.image
```

### Media tools (`generate_*` / `understand_*`) — end to end

A media tool appears only when **both** hold: its model id is set under
`llm.generate.*` / `llm.understand.*`, **and** its name is in `tools.enable`.
Media calls reuse the chat `base_url` + key, so `base_url` typically points at a
gateway that routes each model by id (or use one provider's own ids).

| Tool | Args | Endpoint | Output |
|------|------|----------|--------|
| `generate_image` | `prompt`, `path?`, `size?` | `POST /images/generations` | PNG under `media/image-<ts>.png` |
| `generate_speech` | `text`, `path?`, `voice?` | `POST /audio/speech` | `media/speech-<ts>.mp3` |
| `generate_video` | `prompt`, … | `POST /videos/generations` | `media/video-<ts>.*` |
| `understand_image` | image `path`/`url` + question | vision chat | text (read-only) |
| `understand_voice` | audio `path` | transcription | text (read-only) |
| `understand_video` | video `path` | multimodal | text (read-only) |

`understand_*` are read-only (usable in read-only prompts). `generate_*` write
under `media/` **without** `--allow-write` — being in the enable list is the
write consent for that folder.

Runnable config (`$MOW_HOME/config.yaml`):

```yaml
llm:
  base_url: https://api.openai.com/v1   # or a gateway that routes these models
  api_key_env: OPENAI_API_KEY           # media reuses the chat endpoint + key
  model: gpt-5-mini
  generate:
    image:  gpt-image-1                 # OpenAI ids shown; swap for your gateway's
    speech: tts-1
    speech_voice: alloy                 # provider voice id (OpenAI-style name or vendor voice_id)
    # video: …                          # if your endpoint supports video
  understand:
    image:  gpt-5                       # vision-capable chat model
    voice:  whisper-1
    # video: …
tools:
  enable: [read, glob, grep,
           generate_image, generate_speech, generate_video,
           understand_image, understand_voice, understand_video]
```

Then just ask — the agent calls the tool; you don't invoke it directly:

```bash
mow run -p "Generate an image of a red bicycle on a beach and save it"
#   → generate_image → media/image-….png
mow run -p "Describe the image at diagram.png"
#   → understand_image → text answer
mow run -p "Transcribe recording.mp3 and summarize it"
#   → understand_voice → transcript → summary
```

Confirm what's active with `mow run -p "list your available tools"`. A missing
tool means its model id isn't set or its name isn't in `tools.enable`.

---

## 7. Config and trust

Load order: defaults → explicit `--config` paths → `$MOW_HOME/config.yaml` (default `~/.mow/config.yaml`) → env → trusted project `.mow/config.yaml`.

`MOW_HOME` relocates the user data root (config, sessions, skills, global `AGENTS.md`). Default is `~/.mow`. Useful for tests/CI: `MOW_HOME=$(mktemp -d)`.

Project trust: `mow trust` (stored out-of-band in `$MOW_HOME/trusted`) or env `MOW_TRUST_PROJECT=1` enables project config and `.mow/skills`. Trust is never read from inside the workspace — a cloned repo cannot grant itself trust. Even trusted, project config may not set `llm.base_url`, `llm.wire`, credentials, headers, media model ids (`llm.generate` / `llm.understand`), `session.dir`, `policy.extra_roots`, power tools (`write`/`edit`/`bash`), or media-write tools (`generate_*`). Project `tools.enable` only *adds* safe tools (never replaces the host list). Project `skills.dirs` entries outside the workspace are ignored.

**Supported env (slim set):**

| Env | Purpose |
|-----|---------|
| `MOW_HOME` | User data root |
| `MOW_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM auth |
| `MOW_MODEL` / `OPENAI_MODEL` / `ANTHROPIC_MODEL` | Chat model |
| `MOW_BASE_URL` / `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` | LLM endpoint |
| `MOW_WIRE` | Wire id (optional) |
| `MOW_TRUST_PROJECT` | Trust project `.mow/*` for this invocation (persistent: `mow trust`) |

Workspace, power tools, stream, media models → **yaml** and/or **CLI flags** (`--workspace`, `--allow-write`, `--stream`, …). MCP OAuth automation may use `MOW_MCP_AUTH_CODE` (pack-only).

**Extra FS roots** (optional): paths outside the primary workspace that FS tools may still touch (same symlink jail rules).

| Source | How |
|--------|-----|
| CLI | `--extra-root /path` (repeatable for multiple roots) |
| User config | `policy.extra_roots: [/path, …]` in `$MOW_HOME/config.yaml` or `--config` |
| Embed | `Options.ExtraRoots` |
| Project `.mow/config` | **Not allowed** (stripped like credentials / power tools) |

Relative tool paths resolve against the primary `--workspace`. **Absolute** paths are
allowed under the workspace or an extra root. The system prompt lists configured
extra roots so the model knows they are in the jail (not “restricted”).

Roots are fixed for the life of an Engine: they are read once at `mow.New` and
there is no API to add one mid-session. Grant the root at launch, or start a new
session.

> **Scope of the guarantee.** The path jail applies to the **FS tools**
> (`read`, `write`, `edit`, `glob`, `grep`). It is **not** applied to `bash`,
> which runs real commands with the workspace as cwd and can reach anything the
> user can — a coding agent needs `git`, compilers, and toolchains outside the
> tree. Treat the jail as a guardrail against accidental scope creep and
> confused-deputy edits, **not** as containment against a hostile model or
> prompt injection. `bash` is off by default and enabling it (`--allow-shell`)
> is the real trust decision; for containment, run mow in a container or
> sandbox with the filesystem restricted at the OS level.

An extra root grants the **same** permissions as the workspace: if `--allow-write`
is on, files under an extra root are writable too. There is currently no
read-only root variant, so grant only what the task needs.

Example template: [`internal/config/mow.yaml.example`](../internal/config/mow.yaml.example).

---

## 8. Sessions

JSONL under `session.dir` (default `$MOW_HOME/sessions/<project-hash>/`).  
Default: new session. Resume: `--continue` (latest) or `--session ID` (loads agent prior). `--no-session` for tests/CI. Agent prior uses the last full message snapshot.

Works on **`mow run`** and **`mow tty`** (same `Options.Continue` / `SessionID`). The line session prints `session=…` at start (and a short transcript when resuming) and again on exit with a resume hint (`mow tty --session <id>` or `--continue`).

Embedders build a session picker with **`Engine.Sessions()`** → `[]SessionInfo{ID, Updated, Preview}` (newest first, `Preview` = first user line), then resume via `Options.SessionID`.

**Ephemeral asides:** `PromptWith(ctx, text, PromptOpts{Ephemeral: true})` runs a turn against the current context but does **not** append it to history or the session file, so it never re-enters a later prompt (events/streaming still fire). `mow tty` exposes this as **`/btw <question>`** — a mid-conversation side question that doesn't pollute context. Host UIs can offer the same via `PromptOpts{Ephemeral: true}`.

**Cancel mid tool batch:** hard-abort fails fast (siblings cancelled). Soft results already finished still append to history in call order; incomplete tools are omitted. Session prior keeps whatever was appended before cancel (`StopReason=cancelled`).

**LLM HTTP:** chat/stream requests retry up to 3 times on 429 / 5xx / transient network errors (honours `Retry-After` when present).

---

## 9. Extending

| Mode | Mechanism |
|------|-----------|
| Config-only | yaml, env, skills markdown |
| Tool pack | `ext.RegisterTool` in `init` (blank-import) |
| Hooks | `RegisterPreTool` / `PostTool` / `UserPrompt` / `SessionStart` / `PreCompact` / `AfterTurn` / `Stop` |
| CLI pack | `ext.RegisterCommand` + blank-import in `cmd/mow` |
| Pre-New setup | `ext.RegisterBeforeNew` (e.g. register config-driven tools) |
| Custom binary | `mow.New` + choose which packs to import |

See [extensions.md](extensions.md) for ACP, media, and pack decisions.

---

## See also

- [architecture.md](architecture.md)  
- [extensions.md](extensions.md)

## Usage accounting & inline thinking

Every LLM call's provider-reported token usage is parsed on both wires
(streaming included — OpenAI via `stream_options.include_usage`, Anthropic via
`message_start`/`message_delta`) and summed per run: `RunResult.Usage`, and
`input_tokens`/`output_tokens` on the `run.end` event. Zero means the provider
sent none.

Models that emit chain-of-thought inline as `<think>…</think>` (and known
dialects) are normalized by the loop: committed history, session files, and
`Result.Text` are always tag-free. UIs needing live-stream extraction use
`mow.ExtractThinking` / `mow.StripThinking`.
