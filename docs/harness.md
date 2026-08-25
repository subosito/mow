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
- Not a full IDE agent (goals/MCP live as packs or external hosts).
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
mow-full goal run --goal "…"  # multi-step outer loop (or --id NAME)
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
| `mow` | `New`, `Engine`, `Run`, options/result types, `Tool`/`Message`/`ChatFunc`, `Engine.Extension` (`mow.go` re-exports `internal/engine/`) |
| `ext` | Register tools, hooks, **CLI commands**, BeforeNew (registration only) |
| `cliutil` | Shared CLI flags / `--long` help / `NewEngine` — **not** a pack |
| `extcfg` | Decode `extensions.<name>` — shared by extensions and packs |
| `ext/rpc` | JSON-lines embed protocol + `mow rpc` |
| `ext/acp` | ACP agent + client + `acp_delegate` + `mow acp` |
| `packs/mcp` | MCP servers → tools (config opt-in) |
| `packs/proc` / `packs/cmdhook` | Background proc tools, command hooks |
| `packs/media` | Media tool pack: `generate_*` / `understand_*` (`mow-full`, config-gated) |
| `eval` | Eval/replay fixture library (`github.com/subosito/mow/eval`, root module) for `go test` hosts |
| `packs/goal` | Outer multi-step goals + `mow goal` |
| `packs/job` | Interval / cron jobs + `mow job` |
| `packs/review` | `mow review` / `mow sec` advisory review |
| `packs/ops` | Ops profiles, health, runbooks |
| `cmd/mow` | Thin shell: core commands + blank-import packs |

### Internal

| Package | Responsibility |
|---------|----------------|
| `internal/engine` | Engine construction, prompt/control, public re-export surface |
| `internal/agent` | Loop: messages, tool calls, max turns, abort, compaction |
| `internal/llm` | OpenAI + Anthropic chat; media HTTP (generate/understand) |
| `internal/tools` | Built-in FS/shell tools |
| `internal/config` | yaml + env; `extensions` blobs |
| `internal/policy` | Workspace jail, power-tool gates |
| `internal/session` | JSONL persistence, resume |
| `internal/contextload` | AGENTS.md / CLAUDE.md, skills, project trust |
| `internal/proc` | Detached process implementation (shared by `packs/proc` and goal tools) |

Do **not** import `internal/*` from outside the module’s own packages. Full
module map: [architecture.md](architecture.md).

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

### Stall detection

A run that keeps calling tools without learning anything new burns the whole
turn budget for nothing. The loop watches for that directly.

**What counts as evidence.** After each tool batch the loop records one key per
result: the tool name, its normalized arguments, and a hash of the complete
result text (already bounded by `policy.max_tool_result_chars`). A batch is
**barren** when every key in it is one this run has produced before. Hashing the
full result rather than a prefix matters — tool output routinely shares a long
head (file banners, test preambles, identical grep context), and prefix keys
made unrelated results collide.

Two consequences worth knowing:

- **A poll that returns changing output never stalls.** Same tool, same args,
  different result = new evidence. Watching a file until it changes, or
  re-running a test that now fails differently, is progress.
- **Distinct calls that return the same string are still distinct evidence.**
  Two greps that both find nothing are two facts, not one.

**Three barren batches, then stop.** The run ends with `ErrStuck` →
`StopStuck`, plus a `loop.stall` event carrying the reason. Three, not two:
two identical batches are a plausible retry after a flaky command or a failed
edit; three is a loop.

This is a **backstop, not a knob** — it is a package-level constant with no
config surface, on purpose. It exists to stop pathological spin, not to tune
how persistent an agent is.

Since `max_turns` is unlimited by default, this is the primary liveness guard.
It keys on *evidence* rather than elapsed turns, so it catches a spinning loop
in three batches while never cutting short work that is still making progress.

Note that repeating the *same tool calls* is a separate, softer signal: the
loop injects a nudge message after several identical batches but never stops
for it. Only the evidence signal hard-stops.

The guards described above (`max_turns`, the evidence/`ErrStuck` backstop,
context cancel, and the identical-batch nudge) are engine mechanism and live in
core. The *workflow* heuristics — re-read caps, inventory caps, the
destructive-git block, and the explore-only nag — are not: they ship as the
linked pack `packs/focus` and can be tuned under `extensions.focus` or dropped
entirely by removing its blank import. See docs/extensions.md.

**Hosts:** surface `StopStuck` distinctly from `StopMaxTurns`. They mean
opposite things — out of an explicitly configured budget vs. out of ideas —
and a user who sees "stuck"
should be prompted to add information or change the task, not to raise a limit.

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

### Pre-first-byte wait state

A gateway can hold a request for minutes with no response headers or bytes at
all (some reasoning models stream nothing until the final tool-call JSON), so
a host has no upstream signal to show. Each LLM call therefore emits
`loop.model.wait` at elapsed 0 (`request sent; waiting for response`) and at
widening thresholds while the call stays silent (10s, 30s, then every 30s:
`waiting for first response`; the status bar’s elapsed clock carries the duration), then `loop.model.active`
once — on the first token/reasoning delta, or when the call returns (a
tool-call-only reply streams nothing, so the return itself ends the wait).

This is a **host-side observation of upstream silence, not proof the model is
reasoning**. Hosts must not render the wait state as "thinking".

### Token efficiency (defaults)

| Knob | Default | Effect |
|------|---------|--------|
| `policy.max_turns` | *(unlimited)* | Optional budget: LLM round-trips per Prompt (a turn may batch up to 8 tools). **Unset by default** — a run ends when it is done, stuck, cancelled, or over an explicit spend budget, never merely because it was long. Set a positive value to opt in to a ceiling; yaml `-1` / CLI `--max-turns 0` also mean unlimited. Only enforced when > 0 |
| `policy.max_context_chars` | `100000` | Soft history budget (chars). When still on this default, auto-scales from `GET /v1/models` `context_window` × `compact_ratio`. Explicit positive value = absolute budget. Set `-1` to disable. Compaction pins user task anchors + tools used so the goal is not lost |
| `policy.compact_ratio` | `0.75` | Fraction of gateway `context_window` used as history budget when auto-scaling (1M → ~750k tok-eq before the **hard cap at `MaxContextCharsHardCap` ≈ 400k tok-eq / 1.6M chars**). Clamped to 0.3–0.95. Ignored when `max_context_chars` is an explicit non-default absolute |
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
| `llm.effort` | Reasoning intensity: `none` \| `low` \| `medium` \| `high` (optional body fields; never part of model id). An effort set here (or via `MOW_EFFORT`, `-effort`, `/effort`) is **pinned**: it survives model switches where the catalog allows it, and is always sent as-is. When effort instead comes from the catalog `default_effort` and that tier is `high`/`max`/`xhigh`, mow sends short mechanical prompts (≤120 runes, no complexity keyword like `debug`/`refactor`/`why does`) at `medium` — wire-only, so `Effort()`, the status line, and the session record still report the selected tier. Catalog lookup matches a single provider prefix either way (`cs/gemini-x` ↔ `gemini-x`) so `default_effort` is adopted onto the client and sent as `reasoning_effort` |
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

**Skills** follow the Agent Skills layout (`<dir>/<name>/SKILL.md`, https://agentskills.io/specification). Optional YAML frontmatter (`name`, `description`, `disable-model-invocation`, …) is parsed; only the markdown body is injected. Spec `name` labels the system section when valid; otherwise the folder name. `disable-model-invocation: true` skips first-prompt selection (`--skill` / `/skills <name>` still load it). Skill dir precedence (search order): global `$MOW_HOME/skills` → `skills.dirs` (host/user config) → trusted `workspace/.mow/skills`. Dedup is by lowercased **folder** name with first-directory precedence (not by resolved path), so a name present in both global and user dirs loads once — the first dir's copy wins.

Agent Plugins (`plugin.json` + bundled `skills/` + optional MCP, https://agent-plugins.org/specification) install as folders under `$MOW_HOME/plugins/<id>/` (trusted project: `.mow/plugins/`). Discovery reads `plugin.json`; `skills/` is searched after user/project skill dirs so those names win. `default-skills` / `always` merge into explicit skills. MCP on a plugin is not auto-registered — use `packs/mcp`. `/plugins` lists installs; `/skills` still activates. There is no `plugin install` yet — drop or clone a plugin folder in.

Selection: `skills.selector` (default on) loads only skills whose folder name appears (case-insensitive substring) in the first user prompt. `skills.selector: false` loads all. `skills.explicit` / `--skill <name>` (repeatable) loads named skills unconditionally regardless of the selector — they load at startup, before the first prompt. CLI `--skill` and config `skills.explicit` are merged; unknown names are silently ignored. Name precedence: CLI wins over config (both deduped, first-seen order). `skills.explicit` is host/user-only — a project config may not force-load skills from global/user dirs it does not own.

Mid-session activation: `Engine.ActivateSkills(name...)` loads named skills into the live system prompt for subsequent turns without restarting, merging (deduping by name) with skills already loaded by the first-prompt selector or explicit config/CLI. Unknown names are returned, not errored. `Engine.AvailableSkills()` lists what can be loaded. In the TUI, `/skill` lists names and `/skill <name>...` activates immediately. Activation acquires the prompt mutex, does not mutate committed history, and preserves the selector and explicit skills.

When `skills.selector` is on, the lazy load happens on the first `Prompt` call (not at `Engine.New`); explicit skills merge with prompt-matched skills there. When the selector is off, all skills compile at `Engine.New`. Both compile into the same system segment after AGENTS.md.

When prefix matches the model (e.g. Claude family → “You are Claude Code”), the “You are mow” line is **omitted** so the model does not see two identities. Prefix sets persona; harness rules still apply.

**Media model ids** live in `extensions.media` (owned by `packs/media`), not under
`llm`. The media tools still share `llm.base_url` / `llm.api_key` /
`llm.headers` — only the per-modality model ids are pack config:

```yaml
extensions:
  media:
    generate:
      image: gpt-image-1
      speech: tts-1
      speech_voice: alloy   # ElevenLabs needs a voice_id, not a display name
      video: sora-2
    understand:
      image: gpt-5
      voice: whisper-1
      video: gemini-2.5
```

Because these share the host's chat credential, an untrusted project config
cannot set them: `extensions.media` is dropped wholesale from project overlays.

**Network timeouts:** `llm.first_byte_timeout_sec` (default `300`) bounds how
long a streaming call waits for response headers/first byte — long-reasoning
models can spend minutes thinking before the first SSE chunk. A full
first-byte timeout is a hard, non-retried failure (it does not multiply
across attempts) and the error names the bound and the knob.
`llm.call_timeout_sec` (default `120`) caps one non-streaming attempt. Both
are host/user config only — a project config cannot set them (same trust
class as `llm.base_url`). A host-injected `Options.HTTPClient` takes
precedence over both knobs: mow consults them only when building its own
transport, so a custom client keeps its own timeout semantics.

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
| `bash` | **No** | `--allow-shell` or config. Unsandboxed unless `--sandbox=bwrap` |
| `generate_*` / `understand_*` | **No** | `mow-full` + `packs/media` only. Model ids + names in `tools.enable`; generate writes under `media/` without `--allow-write` |

```yaml
tools:
  enable:
    - read
    - glob
    - grep
    # media names need mow-full (packs/media) plus extensions.media.* model ids
    # - generate_image
    # - understand_image
```

### Media tools (`generate_*` / `understand_*`) — end to end

Media lives in the linked pack `packs/media`, the same category as `packs/mcp`
and `packs/proc` — not a core builtin. The `mow-full` binary blank-imports it
(`_ "github.com/subosito/mow/packs/media"` in `cmd/mow-full/main.go`); lean
`mow` does not. A custom binary that omits the import simply has no media tools.

A media tool appears only when **both** hold: its model id is set under
`extensions.media.generate.*` / `extensions.media.understand.*`, **and** its name is in `tools.enable`.
The pack registers each tool from config at `BeforeNew` time, so enabling
`generate_image` before `extensions.media.generate.image` is set is a no-op — never a
startup error. Media calls reuse the chat `base_url` + key, so `base_url`
typically points at a gateway that routes each model by id (or use one
provider's own ids).

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

Same `$MOW_HOME/config.yaml` works for lean `mow` and `mow-full`. Unused
`extensions.*` keys stay as yaml blobs. Media tools only appear in `mow-full`
when the model id is set and the name is in `tools.enable`. Lean `mow` leaves
those names off the tool list (`mow doctor` reports it).

```yaml
llm:
  model: gpt-5-mini
  base_url: https://api.openai.com/v1   # or a gateway that routes these models
  api_key_env: OPENAI_API_KEY           # media reuses the chat endpoint + key
tools:
  enable:
    - read
    - glob
    - grep
    # - understand_image               # mow-full + extensions.media.understand.image
    # - generate_image
extensions:
  mowi:
    # mow_bin: mow-full                # host yaml only; TUI spawn (not project .mow/)
    theme: catppuccin-mocha
  media:
    understand:
      image: gpt-4o
    # generate:
    #   image: gpt-image-1
    #   speech: tts-1
    #   speech_voice: alloy
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

Confirm what's active with `mow doctor` or `mow run -p "list your available tools"`.
A missing tool means this binary did not link `packs/media`, the model id isn't
set, or the name isn't in `tools.enable`.

---

## 7. Config and trust

Load order: defaults → `$MOW_HOME/config.yaml` (default `~/.mow/config.yaml`) → selected profile `config.yaml` → explicit `--config` paths → env → trusted project `.mow/config.yaml` (restricted) → env again → explicit Options/CLI.

`MOW_HOME` relocates the user data root (config, sessions, skills, global `AGENTS.md`). Default is `~/.mow`. Useful for tests/CI: `MOW_HOME=$(mktemp -d)`.

Project trust: `mow trust` (stored out-of-band in `$MOW_HOME/trusted`) or env `MOW_TRUST_PROJECT=1` enables project config and `.mow/skills`. Trust is never read from inside the workspace — a cloned repo cannot grant itself trust. Even trusted, project config may not set `llm.base_url`, `llm.wire`, credentials, headers, media model ids (`extensions.media`), `session.dir`, `policy.extra_roots`, power tools (`write`/`edit`/`bash`), or media-write tools (`generate_*`). Project `tools.enable` only *adds* safe tools (never replaces the host list). Project `skills.dirs` entries outside the workspace are ignored.

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

> **Terminology.** One jail has exactly one **workspace** — the *main root*:
> the default cwd for `bash`, where relative tool paths resolve, and where
> the session lives. It comes from the first match of: `--workspace` flag
> (set name or path) → `workspace:` in config yaml → `"."` (the cwd at
> startup). Everything else the FS tools may reach is an **extra root**
> (`--extra-root`, `policy.extra_roots`, or a workspace set's
> `extra_roots`). Workspace and extra roots are all *jail roots*; the
> workspace is simply the one relative paths anchor to.

| Source | How |
|--------|-----|
| CLI | `--extra-root /path` (repeatable for multiple roots) |
| CLI | `--workspace NAME` — a named set in `$MOW_HOME/workspaces/<name>/` (hybrid: name or path) |
| User config | `policy.extra_roots: [/path, …]` in `$MOW_HOME/config.yaml` or `--config` |
| Embed | `Options.ExtraRoots` / `Options.Workspace` (set name) |
| Project `.mow/config` | **Not allowed** (stripped like credentials / power tools) |

**Workspace profiles** bundle an operator-owned workspace under
`$MOW_HOME/workspaces/<name>/`:

```text
$MOW_HOME/workspaces/monorepo/
├── workspace.yaml   # root + extra_roots
├── config.yaml      # optional normal mow config overlay
├── AGENTS.md        # optional operator instructions
└── skills/          # optional <name>/SKILL.md entries
```

```yaml
# workspace.yaml
root: ~/code/app
extra_roots:
  - ~/code/shared
  - ~/code/vendor:ro
```

`--workspace` is hybrid: a profile name selects this bundle; an existing
directory remains a plain workspace path. The legacy `$MOW_HOME/workspaces/<name>/`
registry is no longer loaded. Profile `config.yaml` is operator-controlled and
may configure normal host settings including profile-scoped `extensions.acp`
`agents` and `mow_agents`. Profile skills take precedence over global/configured
and trusted project skills with the same name.

Relative tool paths resolve against the primary `--workspace`. **Absolute** paths are
allowed under the workspace or an extra root. The system prompt lists configured
extra roots so the model knows they are in the jail (not “restricted”).

Roots are fixed for the life of an Engine: they are read once at `mow.New` and
there is no API to add one mid-session. Grant the root at launch, or start a new
session.

> **Scope of the guarantee.** The path jail applies to the **FS tools**
> (`read`, `write`, `edit`, `glob`, `grep`). It is **not** applied to `bash`
> by default. `bash` / `proc_start` run real commands with the workspace as
> cwd and can reach anything the user can — a coding agent needs `git`,
> compilers, and toolchains outside the tree. Treat the FS jail as a
> guardrail against accidental scope creep and confused-deputy edits, **not**
> as containment against a hostile model or prompt injection.
>
> `bash` / `proc_start` are off by default. `--allow-shell` is the real trust
> cliff: those tools are **not path-jailed**. The process runs as you
> (`cd /; curl | sh` is allowed). File tools stay in the workspace jail; this
> flag does not. Treat it like root-adjacent, not a sibling of
> `--allow-write`.
>
> **Opt-in OS jail (`--sandbox`).** When you also pass `--sandbox` (or
> explicitly `--sandbox=bwrap`; config: `policy.sandbox: bwrap`), both `bash`
> and `proc_start` are wrapped in
> [bubblewrap](https://github.com/containers/bubblewrap) on Linux. The
> workspace and extra roots are bound the same way as the FS jail (`:ro` →
> `--ro-bind`); `$HOME` is **not** bound unless it is one of those roots. A
> missing `bwrap` binary is a hard error — mow never silently falls back to a
> raw shell. This is a **filesystem/home jail, not a VM and not malware
> containment**: network stays on (no `--unshare-net`), so `curl | sh` still
> works. macOS has no backend, so the flag is not registered there at all.
> Default remains unsandboxed (`none`). For stronger containment, run all of
> mow in a container with the filesystem restricted at the OS level.

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

**Steer mid turn:** `Engine.Steer(text)` interrupts only the in-flight LLM call and reissues it with the steer appended as a user message — finished tool results stay in history (no cancel/restart). Delivered while a run is live (from `EventRunStart` / `Status().Busy` until run end); sent before a run starts it is dropped rather than leaking into the next turn. Drained at turn boundaries, so several steers arrive together in order. See [embedding.md §9](embedding.md).

**LLM HTTP:** chat/stream requests retry up to 3 times on 429 / 5xx / transient network errors (honours `Retry-After` when present).

---

## 9. Extending

| Mode | Mechanism |
|------|-----------|
| Config-only | yaml, env, skills markdown |
| Tool pack | `ext.RegisterTool` in `init` (blank-import) |
| Hooks | `RegisterPreTool` / `PostTool` / `UserPrompt` / `SessionStart` / `PreCompact` / `AfterTurn` / `Stop` |
| CLI pack | `ext.RegisterCommand` + blank-import in `cmd/mow-full` (and `cmd/mow` for the lean set) |
| Pre-New setup | `ext.RegisterBeforeNew` (e.g. register config-driven tools) |
| Custom binary | `mow.New` + choose which packs to import |

**Tool parameter schemas are sanitized before they reach a provider.** Tools
arriving from MCP servers, ACP peers, or integrators carry ordinary JSON
Schema, document metadata included. OpenAI-compatible endpoints ignore the
extra keys; stricter validators reject the entire request over one of them —
so a single third-party tool takes every other tool down with it, with an error
that names an array index rather than a tool:

```
HTTP 400: Invalid JSON payload received. Unknown name "$schema" at
'request.tools[0].function_declarations[17].parameters': Cannot find field.
```

The agent loop strips `$schema`, `$id`, `$comment`, `$anchor`, `$dynamicAnchor`,
and `$vocabulary` recursively. These describe the schema document rather than
the parameters, so removing them cannot change how a model calls the tool.
`$ref` and `$defs` are kept: dropping a reference would silently widen a
parameter from a fixed shape to anything, which is worse than the 400 it would
avoid. Schemas with nothing to remove pass through byte-identical.

The pass is unconditional rather than gated on a provider id — mow cannot see
what is behind an OpenAI-compatible base URL, and the endpoint that produced
the error above presented itself as one.

See [extensions.md](extensions.md) for ACP, media, and pack decisions.

---

## See also

- [architecture.md](architecture.md)  
- [extensions.md](extensions.md)

## Usage accounting & inline thinking

Every LLM call's provider-reported token usage is parsed on both wires
(streaming included — OpenAI via `stream_options.include_usage`, Anthropic via
`message_start`/`message_delta`) and summed per run: `RunResult.Usage`, and
`input_tokens`/`output_tokens` on the `loop.run.end` event. Zero means the provider
sent none.

Models that emit chain-of-thought inline as `<think>…</think>` (and known
dialects) are normalized by the loop: committed history, session files, and
`Result.Text` are always tag-free. UIs needing live-stream extraction use
`mow.ExtractThinking` / `mow.StripThinking`.

## Untrusted output framing

External tool bodies (`bash`, `acp_delegate`, MCP server tools that opt in) are
wrapped before they enter model history:

```text
<untrusted-output source="bash" nonce="…">
…tool stdout…
</untrusted-output>
```

The per-engine nonce is also mentioned in the system prompt so the model treats
framed text as data, not instructions. A forged closing tag inside the body is
neutralized. Workspace file reads are **not** framed (they are already under
the path jail / trust boundary).

## Context archive

When history is compacted (automatic soft compact or `Engine.Compact`), mow
writes a plain-text archive under the session dir:

```text
\$MOW_HOME/sessions/<project>/<session-id>.archive/0001-….md
```

Archives are append-only Markdown snapshots of what left the live window —
host/TUI material for review, not something the agent reads back. No search
tool ships with the spine: hosts that want pattern search over archives
build it on the ext seam (a read-only tool resolving the session dir from
the engine), and mow's own `Engine.Sessions()` listing covers resume.

### MCP as untrusted output

MCP tool results are external server text. The MCP pack marks every `mcp_*`
tool with `Untrusted() bool` so the agent loop frames results in
`<untrusted-output>` the same way as `bash` and `acp_delegate`.

