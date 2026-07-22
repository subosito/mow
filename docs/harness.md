# mow harness — end-to-end design

**Binary:** `mow`  
**Module:** `github.com/subosito/mow`  

Stack context: [architecture.md](architecture.md). Packs: [extensions.md](extensions.md).

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
mow repl                 # line REPL (core)
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
  base_url: http://127.0.0.1:9420/v1
  api_key_env: OPENAI_API_KEY   # gateway key
  model: deepseek-v4-flash      # model id
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
| Child-only timeout (e.g. bash 60s) | Soft error if parent ctx still alive |
| `mow repl` Ctrl+C | Cancels **current turn only**; REPL stays up for the next prompt |
| `mow run` Ctrl+C | Exit code 130 |

Lifecycle slog (`mow run/tool start|end`) is **Debug** by default. Stock CLI prints compact progress on stderr (`→ read path`, `→ grep pattern`) via `OnEvent`; use `--verbose` for Debug logs.

### Token efficiency (defaults)

| Knob | Default | Effect |
|------|---------|--------|
| `policy.max_turns` | `120` | Optional budget: LLM round-trips per Prompt (a turn may batch up to 8 tools). Default 120 for casual use. `0` / CLI `--max-turns 0` / yaml `-1` = **no turn limit** (hours/days OK; stop with Ctrl+C). Only enforced when > 0 — no hidden safety cap on unlimited |
| `policy.max_context_chars` | `100000` | Soft-compact history before each LLM call; set `-1` to disable |
| `policy.max_tool_result_chars` | `24000` | Cap each tool result stored for the model (~6k tokens) |
| `policy.max_read_bytes` | `512KiB` | Cap `read` tool raw file size |
| `policy.max_parallel_tools` | `8` | Concurrent tool Exec per assistant batch; `1` = sequential |
| Loop truncate | always | Oversized tools trimmed even under context budget |

Compaction is **character-estimate**, not a real tokenizer. It keeps the system message + recent turns (aligned on a user boundary), stubs the middle, and shrinks older tool bodies first. Use `RegisterPostTool` for smarter summarization when needed.

**Hooks concurrency:** when `max_parallel_tools > 1`, PreTool/PostTool may run on multiple tools at once. Keep hooks non-blocking and concurrency-safe (the built-in event emitter is).

---

## 5. LLM client

| Config | Meaning |
|--------|---------|
| `llm.base_url` | Provider or gateway `/v1` |
| `llm.api_key` / `api_key_env` | Provider key **or** gateway key |
| `llm.model` | Model id |
| `llm.wire` | `openai-chat-completions` (default) \| `openai-responses` \| `anthropic-messages` |
| `llm.headers` | Optional extra headers |
| `llm.stream` | SSE content deltas (both wires) |
| `llm.generate.*` | Side-lane model ids for generate tools |
| `llm.understand.*` | Side-lane model ids for understand tools |

No provider OAuth in mow. Streaming: `OnToken` / `OnEvent` / ACP `session/update` chunks.

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

---

## 7. Config and trust

Load order: defaults → explicit `--config` paths → `$MOW_HOME/config.yaml` (default `~/.mow/config.yaml`) → env → trusted project `.mow/config.yaml`.

`MOW_HOME` relocates the user data root (config, sessions, skills, global `AGENTS.md`). Default is `~/.mow`. Useful for tests/CI: `MOW_HOME=$(mktemp -d)`.

Project trust: `mow trust` (stored out-of-band in `$MOW_HOME/trusted`) or env `MOW_TRUST_PROJECT=1` enables project config and `.mow/skills`. Trust is never read from inside the workspace — a cloned repo cannot grant itself trust. Even trusted, project config may not set `llm.base_url`, credentials, headers, `session.dir`, or enable power tools.

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

Example template: [`internal/config/mow.yaml.example`](../internal/config/mow.yaml.example).

---

## 8. Sessions

JSONL under `session.dir` (default `$MOW_HOME/sessions/<project-hash>/`).  
Default: new session. Resume: `--continue` (latest) or `--session ID` (loads agent prior). `--no-session` for tests/CI. Agent prior uses the last full message snapshot.

Works on **`mow run`** and **`mow repl`** (same `Options.Continue` / `SessionID`). REPL prints `session=…` at start (and a short transcript when resuming) and again on exit with a resume hint (`mow repl --session <id>` or `--continue`).

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
