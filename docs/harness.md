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
- Not a full IDE application (review/ops/media workflows belong in external hosts).
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
mow tty                  # optional line session (ext/tty)
mow acp                  # ACP agent for host UIs (ext/acp)
# External applications can layer multi-step workflows over the Engine API.
mow run -p "…"           # one-shot (ext/cli)
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

### E. ACP (extension)

```text
Editor ──stdio ACP──▶ mow acp ──▶ Engine
mow loop ──tool delegate──▶ peer ACP process
```

---

## 3. Package inventory

### Public

| Package | Responsibility |
|---------|----------------|
| `mow` | `New`, `NewHarness`, `Engine`, `Run`, options/result types, `Tool`/`Message`/`ChatFunc`, `Engine.Extension` (`mow.go` re-exports `internal/engine/`) |
| `ext` | Register tools, hooks, **CLI commands**, BeforeNew (registration only) |
| `cliutil` | Shared CLI flags / `--long` help / `NewEngine` → `NewHarness` — **not** a pack |
| `extcfg` | Decode `extensions.<name>` for linked extensions |
| `ext/cli` | Unix dispatcher (`Main`) + `run`, `trust`, `doctor`, `approvals`, version, help |
| `ext/tty` | Optional line REPL (`mow tty`) |
| `ext/acp` | ACP agent + extras + client + `delegate` + `mow acp` (does not import `ext/cli`) |
| `ext/mcp` | MCP servers → tools (config opt-in) |
| `ext/proc` / `ext/cmdhook` | Background proc tools and command hooks |
| `eval` | Eval/replay fixture library (`github.com/subosito/mow/eval`, root module) for `go test` hosts |
| `cmd/mow` | Import list: `ext/cli.Main` + blank-imported core extensions |

### Internal

| Package | Responsibility |
|---------|----------------|
| `internal/engine` | Engine construction, prompt/control, public re-export surface |
| `internal/agent` | Loop: messages, tool calls, max turns, abort, compaction |
| `internal/llm` | OpenAI + Anthropic chat HTTP |
| `internal/tools` | Built-in FS/shell tools |
| `internal/config` | yaml + env; `extensions` blobs |
| `internal/policy` | Workspace jail, power-tool gates |
| `internal/session` | JSONL persistence, resume |
| `internal/contextload` | AGENTS.md / CLAUDE.md, skills, project trust |
| `internal/proc` | Detached process implementation used by `ext/proc` |

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

Exploration also has built-in checkpoints, with no configuration required:

- After six exploration batches, two minutes of exploration, or two
  low-progress batches, the model is asked to state its leading explanation and make only targeted checks.
- Checkpoints are advisory only: elapsed time, overlapping reads, and searches
  with no matches never remove tools or force a final answer. A miss can be useful
  evidence when investigating multiple repositories. The independent exact-repeat
  backstop still stops consecutive batches that repeat previously seen calls/results.
- Read-only extension tools and MCP discovery/search tools count as exploration,
  including search tools that maintain indexes and advertise `readOnlyHint: false`.
  This classification affects advisory guidance only, not execution permissions.
- Successful edits, writes, other application-specific tools, and recognized test or
  build commands reset the exploration streak. Cancellation and explicit spend
  limits still take precedence. This is not a wall-clock limit on the whole run.

These checkpoints complement the exact-repeat backstop above. They live in
core so stock CLI and ACP hosts receive the same behavior. Optional policy
extensions may still add result degradation, inventory caps, or workflow rules.

Grep keeps compact default results. Use `max_results: 100` to request up to
100 matching lines without the default per-file display caps, or
`files_only: true` to list matching filenames (default 100). `max_results`
accepts 1–500 for either mode. Filenames count once regardless of matches in
each file. Truncation is explicit; narrow the path or pattern when the cap is
reached. Normal file-jail, excluded-directory, file-size, and history-output
limits still apply.

The `grep` tool searches literal text by default. Pass `regex: true` for regular
expressions such as `model|effort` or escaped symbols. Invalid expressions are
reported as tool errors; literal misses containing regex punctuation include
an actionable reminder. Both ripgrep and the fallback walker support this mode
with the same filesystem jail and result caps.

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
| `policy.max_turns` | *(unlimited)* | Optional budget: LLM round-trips per Prompt (a turn may batch up to 8 tools). **Unset by default** — a run ends when it is done, stuck, cancelled, or over an explicit spend budget, with built-in checkpoints for prolonged exploration. Set a positive value to opt in to a ceiling; yaml `-1` / CLI `--max-turns 0` also mean unlimited. Only enforced when > 0 |
| Auto-compact | always on when `context_window` is known | Trips at 80% of `context_window` in **tokens** (same units as the host ctx chip). Occupancy is last billed input tokens, or history chars ÷ calibrated chars/token when the provider has not reported usage yet. Compaction pins user task anchors + tools used. Not a yaml knob — Go `Options.MaxContextChars` can override (char budget, tests) or disable (`< 0`). Unknown window: off (tool-result trim still runs). Manual `/compact` still has a default char target |
| `llm.context_window` / `input_price` / `output_price` | (optional) | Override metering; otherwise `Engine.Limits()` uses `GET /v1/models` fields (`context_window`, `pricing.input_per_mtok` / `output_per_mtok`) — no client-side price table |
| `policy.max_tool_result_chars` | `24000` | Cap each tool result stored for the model (~6k tokens) |
| `policy.compact_at_tokens` | `400000` | Lowers the auto-compact trip to this many tokens when it is below 80% of the window. Every call re-sends the whole history, so on a 1M window this bounds per-call input spend. `-1` = window only. A project may only lower it |
| `policy.max_read_bytes` | `512KiB` | Cap `read` tool raw file size |
| `policy.bash_timeout_sec` | `300` | Per bash call. A coding agent runs builds and test suites, so the default is minutes; a single call may request longer via the tool's `timeout_sec` argument |
| `policy.max_bash_timeout_sec` | `900` | Ceiling on a per-call `timeout_sec` request, so a hung command cannot park the loop |
| `policy.max_parallel_tools` | `8` | Concurrent tool Exec per assistant batch; `1` = sequential |
| Loop truncate | always | Oversized tools trimmed even under context budget |

Compaction **trips on tokens vs `context_window`**. There is no client tokenizer: billed `input_tokens` from the last call is preferred, otherwise history chars ÷ a smoothed chars/token ratio (seeded at ~4, updated from provider usage so dense code does not under-count). It keeps the system message + **task anchors** (substantive user intents) + recent turns (aligned on a user boundary), stubs the middle (including which tools ran in the dropped span), and shrinks older tool bodies first. After a compact it aims at ~70% of the tripwire so the next tool batch has headroom.

**PreCompact hooks** (`RegisterPreCompact` / `Options.Hooks.OnPreCompact`) already run when history exceeds the budget: skip compaction, or supply a better `Summary` (event includes `Messages` on the public API). Use that for LLM distill if the default stub is not enough; anchors still pin user intents either way.

**Hooks concurrency:** when `max_parallel_tools > 1`, PreTool/PostTool may run on multiple tools at once. Keep hooks non-blocking and concurrency-safe (the built-in event emitter is).

---

## 5. LLM client

| Config | Meaning |
|--------|---------|
| `llm.base_url` | Provider or gateway `/v1` |
| `llm.providers` / `llm.provider` | Named endpoint overlays (`base_url`, key, wire, default model). `--provider` / `MOW_PROVIDER` select one; live `llm.*` is the default. Host/user only |
| `llm.api_key` / `api_key_env` | Provider key **or** gateway key |
| `llm.model` | Model id |
| `llm.effort` | Reasoning intensity: `none` \| `low` \| `medium` \| `high` (optional body fields; never part of model id). An effort set here (or via `MOW_EFFORT`, `-effort`, `/effort`) is **pinned**: it survives model switches where the catalog allows it, and is always sent as-is. When effort instead comes from the catalog `default_effort` and that tier is `high`/`max`/`xhigh`, mow sends short mechanical prompts (≤120 runes, no complexity keyword like `debug`/`refactor`/`why does`) at `medium` — wire-only, so `Effort()`, the status line, and the session record still report the selected tier. Catalog lookup matches a single provider prefix either way (`gateway/gemini-x` ↔ `gemini-x`) so `default_effort` is adopted onto the client and sent as `reasoning_effort` |
| `llm.wire` | `openai-chat-completions` (default) \| `openai-responses` \| `anthropic-messages`. If unset, Engine aligns to the **catalog preferred wire** for `llm.model` after `GET /v1/models` (e.g. `claude-sonnet-4` → `anthropic-messages`). Explicit `llm.wire` / `MOW_WIRE` is never overridden |
| `llm.headers` | Optional extra headers. Engine also sends `X-Mow-Actor` (default `mow`), `X-Mow-Component` (`turn.chat` / `turn.compact`), and `X-Mow-Session` (the JSONL session id). Omitted when `--no-session`. Gateways may treat these as labels; plain providers ignore them |
| `llm.stream` | SSE content deltas (both wires) |
| `llm.prompt_cache` | Provider prompt caching (default on). **Only effective on `anthropic-messages`**: mow sends `cache_control` on system / tools / last message so Anthropic bills repeated prefixes as cache reads (~90% cheaper input). On `openai-chat-completions` mow does not emit Anthropic cache breakpoints — a gateway **O2A** translator (`adapter:wire-translate-o2a`) typically shows `cache_read_tokens=0` and full input rates. Prefer native `anthropic-messages` for Claude. Set `false` only if a gateway rejects `cache_control` |
| `llm.system_prefix` | Optional text segments prepended **before** the compiled system (each list item separate). For product identity / provider preambles. User config only — not project `.mow/config` |
| `llm.system_prefix_models` | Case-insensitive globs limiting when `system_prefix` applies. **Empty = every model** when prefix is set. Re-evaluated each call (follows `SetModel`) |

**System prompt composition** (request order):

1. `llm.system_prefix` segments (optional) — product identity / provider preambles (“You are …”)
2. **Identity line** — only when **no** system_prefix applies to the active model: `You are mow, …`
3. **Harness operating rules** (always) — workspace-agnostic; no second identity
4. `~/.agents/AGENTS.md` (shared base) + `$MOW_HOME/AGENTS.md` (mow overlay) + `{dir}/.agents/AGENTS.md` then `AGENTS.md` / `CLAUDE.md` (walk, closer later)
   The ancestor walk is bounded: it stops at the nearest enclosing **git root** (a directory holding `.git`), or `$HOME` when there is none, and never goes above either — an unrelated parent checkout's `AGENTS.md` is not project instruction. A workspace outside both reads only its own files. Hermetic engines (`Options.LoadUserConfig=false`) do not walk at all: workspace files only.
5. Skills + per-call `SystemAppend`

**Skills** follow the Agent Skills layout (`<dir>/<name>/SKILL.md`, https://agentskills.io/specification). Optional YAML frontmatter (`name`, `description`, `disable-model-invocation`, …) is parsed; only the markdown body is injected. Spec `name` labels the system section when valid; otherwise the folder name. `disable-model-invocation: true` skips first-prompt selection (`--skill` / `/skills <name>` still load it). Skill dir precedence (search order, first name wins): `$MOW_HOME/skills` → profile → `skills.dirs` → trusted `workspace/.mow/skills` → trusted `workspace/.agents/skills` → `~/.agents/skills` → plugin `skills/`. Shared `~/.agents` fills gaps; a mow-owned folder of the same name wins. One level only (`<dir>/<name>/SKILL.md`), not a recursive catalog walk.

Agent Plugins (`plugin.json` or `.claude-plugin/plugin.json` + bundled `skills/` + optional MCP/hooks, https://agent-plugins.org/specification) install as folders with this discovery precedence: global `$MOW_HOME/plugins/<id>/` → active workspace profile `$MOW_HOME/workspaces/<name>/plugins/<id>/` → trusted project `workspace/.mow/plugins/<id>/`. The workspace-profile root applies when `--workspace <name>` selects that profile (including plugins-only profiles with no `config.yaml`); unlike the project root, it is operator-owned and needs no `mow trust`. Bundled `skills/` are searched after user/project skill dirs so those names win. `default-skills` / `always` merge into explicit skills. Host-owned plugins (global + profile) auto-start MCP (`mcpServers`) and cmdhook (`hooks/hooks.json`). Plugin MCP YAML of the same server name wins. Project `.mow/plugins` is skills-only — MCP/hooks do not auto-spawn from the workspace. Plugin MCP/cmdhook set `CLAUDE_PLUGIN_ROOT` and, when unset, `CLAUDE_CONFIG_DIR=$MOW_HOME`. `/plugins` lists installs. Place folders under `$MOW_HOME/plugins`, a workspace profile, or trusted `.mow/plugins`.

Selection: `skills.selector` (default on) loads only skills whose folder name appears (case-insensitive substring) in the first user prompt. `skills.selector: false` loads all. `skills.explicit` / `--skill <name>` (repeatable) loads named skills unconditionally regardless of the selector — they load at startup, before the first prompt. CLI `--skill` and config `skills.explicit` are merged; unknown names are silently ignored. Name precedence: CLI wins over config (both deduped, first-seen order). `skills.explicit` is host/user-only — a project config may not force-load skills from global/user dirs it does not own.

Mid-session activation: `Engine.ActivateSkills(name...)` loads named skills into the live system prompt for subsequent turns without restarting, merging (deduping by name) with skills already loaded by the first-prompt selector or explicit config/CLI. Unknown names are returned, not errored. `Engine.AvailableSkills()` lists what can be loaded. Activation acquires the prompt mutex, does not mutate committed history, and preserves the selector and explicit skills.

When `skills.selector` is on, the lazy load happens on the first `Prompt` call (not at `Engine.New`); explicit skills merge with prompt-matched skills there. When the selector is off, all skills compile at `Engine.New`. Both compile into the same system segment after AGENTS.md.

When prefix matches the model (e.g. Claude family → “You are Claude Code”), the “You are mow” line is **omitted** so the model does not see two identities. Prefix sets persona; harness rules still apply.

## 6. Built-in tools

| Tool | Default? | Notes |
|------|----------|--------|
| `read`, `glob`, `grep` | **Yes** | Secure defaults |
| `write`, `edit` | **No** | `--allow-write` or config |
| `bash` | **No** | `--allow-shell` or config. Unsandboxed unless `--sandbox` |

```yaml
tools:
  enable:
    - read
    - glob
    - grep
```

## 7. Config and trust

Load order: defaults → `$MOW_HOME/config.yaml` (default `~/.mow/config.yaml`) → selected profile `config.yaml` → explicit `--config` paths → env → trusted project `.mow/config.yaml` (restricted) → env again → explicit Options/CLI.

`MOW_HOME` relocates the user data root (config, sessions, skills, global `AGENTS.md`). Default is `~/.mow`. Useful for tests/CI: `MOW_HOME=$(mktemp -d)`.

Project trust: `mow trust` (stored out-of-band in `$MOW_HOME/trusted`) or env `MOW_TRUST_PROJECT=1` enables project config and `.mow/skills`. Trust is never read from inside the workspace — a cloned repo cannot grant itself trust. Even trusted, project config may not set `llm.base_url`, `llm.wire`, credentials, headers, `session.dir`, `policy.extra_roots`, `policy.env_passthrough`, or power tools (`write`/`edit`/`bash`). Spend and prompt-shape decisions are host/user too: `llm.model`, `llm.effort`, `llm.stream`, and `skills.selector` are dropped from a project overlay, and `policy.max_turns` is accepted only when it *lowers* the host ceiling (a higher cap, or `-1` for unlimited, is ignored). Project `tools.enable` only *adds* safe tools (never replaces the host list). Project `skills.dirs` entries outside the workspace are ignored.

**Supported env (slim set):**

| Env | Purpose |
|-----|---------|
| `MOW_HOME` | User data root |
| `MOW_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM auth |
| `MOW_MODEL` / `OPENAI_MODEL` / `ANTHROPIC_MODEL` | Chat model |
| `MOW_PROVIDER` | `llm.providers` name (overlays url/key/wire/model) |
| `MOW_BASE_URL` / `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` | LLM endpoint |
| `MOW_WIRE` | Wire id (optional) |
| `MOW_TRUST_PROJECT` | Trust project `.mow/*` for this invocation (persistent: `mow trust`) |
| `MOW_DEBUG_CACHE` | Debug knob: `1` logs prompt-cache prefix drift per turn (hash per message; only useful when chasing a cache-write bill) |

Workspace, power tools, and stream → **yaml** and/or **CLI flags** (`--workspace`, `--allow-write`, `--stream`, …). MCP OAuth automation may use `MOW_MCP_AUTH_CODE`.

### Extra FS roots

Optional: paths outside the primary workspace that FS tools may still touch (same symlink jail rules).

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
`peers`. Profile skills take precedence over global/configured
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
> **Credential scrub.** Child processes never inherit mow's own credentials.
> Removed from the environment of `bash`, `proc_start`, the ACP terminal
> (`terminal/create`), command hooks, and MCP stdio servers:
> `MOW_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, anything ending in
> `_API_KEY`, `_TOKEN`, `_SECRET`, `_SECRET_KEY`, or `_SECRET_ACCESS_KEY`,
> `AWS_SESSION_TOKEN`, and the names mow reads model keys from
> (`llm.api_key_env` and every `llm.providers[*].api_key_env`, whatever they
> are called). An MCP server's own `env:` map is applied afterwards, so an
> operator can still hand one server its own token deliberately. When a build
> genuinely needs one — a package registry or CI token — name it exactly in
> `policy.env_passthrough: [NPM_TOKEN, …]`; the passthrough list wins over the
> scrub. Exact names only, no globs; it is host/user config (stripped from
> project `.mow/config`, like `policy.extra_roots`).
>
> **Opt-in OS jail (`--sandbox`).** `bash`, `proc_start`, and the ACP terminal
> (`terminal/create`) use
> Fence on Linux and macOS (config: `policy.sandbox: fence`). Install Fence
> first; missing required isolation is a hard error, never an unsandboxed
> fallback. Mow supplies workspace/extra-root permissions and denies outbound
> network access. Default remains unsandboxed (`none`). See the Fence section
> below for policy scope and limitations.

An extra root grants the **same** permissions as the workspace: if `--allow-write`
is on, files under an extra root are writable too. There is currently no
read-only root variant, so grant only what the task needs.

Example template: [`internal/config/mow.yaml.example`](../internal/config/mow.yaml.example).

---

## 8. Sessions

JSONL under `session.dir` (default `$MOW_HOME/sessions/<project-hash>/`).  
Default: new session. Resume: `--continue` (latest) or `--session ID` (loads agent prior). `--no-session` for tests/CI. Agent prior uses the last full checkpoint plus subsequent incremental records. Resume also restores the last model, effort, and tool policy (write/shell/read-only) unless you pass an explicit `--model` / `--effort` / `--read-only` / `--allow-*` / `--disallow-*` flag.

Normal conversation growth stores only new messages. Full checkpoints are
appended after edits, compaction, a writer change, or a doubling of history
size. Existing full-snapshot and per-message JSONL sessions remain readable;
new incremental records require a version of mow that understands them.
Session readers stream records instead of buffering the whole file. Raw
history remains append-only, including superseded turns.

Works on **`mow run`** and **`mow tty`** (same `Options.Continue` / `SessionID`). The line session prints `session=…` at start (and a short transcript when resuming) and again on exit with a resume hint (`mow tty --session <id>` or `--continue`).

Embedders build a session picker with **`Engine.Sessions()`** → `[]SessionInfo{ID, Updated, Preview}` (newest first, `Preview` = first user line), then resume via `Options.SessionID`.

**Ephemeral asides:** `PromptWith(ctx, text, PromptOpts{Ephemeral: true})` runs a turn against the current context but does **not** append it to history or the session file, so it never re-enters a later prompt (events/streaming still fire). `mow tty` exposes this as **`/btw <question>`** — a mid-conversation side question that doesn't pollute context. Host UIs can offer the same via `PromptOpts{Ephemeral: true}`.

**Cancel mid tool batch:** hard-abort fails fast (siblings cancelled). Soft results already finished still append to history in call order; incomplete tools are omitted. Session prior keeps whatever was appended before cancel (`StopReason=cancelled`).

**Steer mid turn:** `Engine.Steer(text)` interrupts only the in-flight LLM call and reissues it with the steer appended as a user message — finished tool results stay in history (no cancel/restart). Delivered while a run is live (from `EventRunStart` / `Status().Busy` until run end); sent before a run starts it is dropped rather than leaking into the next turn. Drained at turn boundaries, so several steers arrive together in order. See [embedding.md §9](embedding.md).

**LLM HTTP:** chat/stream requests retry up to 3 times on 429 / 5xx / transient network errors (honours `Retry-After` when present).

After a streaming completion event, mow allows up to 250 ms and 64 KiB of
trailing HTTP data before closing the body. This gives gateways time to finish
normally instead of observing an immediate client reset after a successful
answer. Cancellation skips or interrupts that cleanup; a lingering connection
cannot keep the turn open indefinitely. Gateways should distinguish protocol
completion from genuine mid-response cancellation when recording status 499.

---

## 9. Extending

| Mode | Mechanism |
|------|-----------|
| Config-only | yaml, env, skills markdown |
| Tool extension | `ext.RegisterTool` in `init` (blank-import) |
| Hooks | `RegisterPreTool` / `PostTool` / `UserPrompt` / `SessionStart` / `PreCompact` / `AfterTurn` / `Stop` |
| CLI extension | `ext.RegisterCommand` + blank-import in the host binary |
| Pre-New setup | `ext.RegisterBeforeNewContext` (register config-driven tools with resolved authority) |
| Custom binary | `mow.New` + choose which extensions to import |

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

See [extensions.md](extensions.md) for ACP, MCP, and extension decisions.

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

External tool bodies (`bash`, `delegate`, MCP server tools that opt in) are
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
host presentation material, not something the agent reads back. No search
tool ships with the spine: hosts that want pattern search over archives
build it on the ext seam (a read-only tool resolving the session dir from
the engine), and mow's own `Engine.Sessions()` listing covers resume.

### MCP as untrusted output

MCP tool results are external server text. The MCP pack marks every `mcp_*`
tool with `Untrusted() bool` so the agent loop frames results in
`<untrusted-output>` the same way as `bash` and `delegate`.

### Fence shell sandbox

`--allow-shell --sandbox=fence` (or host `policy.sandbox: fence`) wraps both
`bash` and `proc_start` using an installed Fence CLI. Bare `--sandbox` selects
Fence on Linux and macOS. Native file tools retain their existing path jail;
MCP servers and the engine itself are outside this shell backend.

Mow generates the Fence policy from its workspace and extra roots, including
read-only restrictions. Repository and user Fence policies are not loaded.
Network is denied, including outbound localhost; domain allowlists and port
exposure are not configurable in this initial backend. Missing Fence, missing
Linux bubblewrap, or a failed network-namespace probe is an error. Mow will not
accept Fence's proxy-only network fallback. macOS uses Fence's Seatbelt backend.

System toolchain paths remain readable. Environment forwarding uses a
small runtime allowlist, so `env_passthrough` does not widen it. Fence's
mandatory filesystem protections still apply. Command-name deny rules are
not enabled: on systems with multicall utilities those rules can disable
unrelated commands. This does not provide resource limits or hostile-code
containment. Root paths containing glob metacharacters are rejected; read-only
parents cannot be widened by writable child roots under Fence's deny precedence.

The adapter supplies settings through an inherited descriptor, without writing
persistent policy files. Integration was exercised with Fence 0.1.67 on Linux.
Run `MOW_TEST_FENCE=1 devenv shell -- go test -count=1 ./internal/sandbox -run TestFence`
to include live filesystem, network, and child-cleanup checks. macOS enforcement
requires separate validation on a macOS host.

The former `bwrap`/`bubblewrap` backend values are rejected with migration guidance.
Use `--sandbox`, `--sandbox=fence`, or `policy.sandbox: fence` instead.
