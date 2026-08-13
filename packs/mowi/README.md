# mowi

**mow with interface** — an interactive terminal UI (Bubble Tea) for the
[mow](https://github.com/subosito/mow) agent harness. The agent loop, tools,
policies, and sessions all live in mow; mowi is the face you drive them with.

```
mowi            start an interactive session in the current directory
mowi --help     everything below, plus pack subcommands (goal, review, ops, …)
```

## Build & run

mowi is a nested Go module inside the mow repository (Go 1.26.4+; prefer
`devenv shell`, which pins the toolchain):

```bash
just build-mowi     # → bin/mowi (from the repo root)
bin/mowi            # interactive TUI
bin/mowi trust      # manage trusted workspaces
```

Configuration is the shared `$MOW_HOME` config (default `~/.mow`). Point mow
at any OpenAI/Anthropic-compatible endpoint, e.g.:

```yaml
llm:
  base_url: https://api.openai.com/v1     # or your OpenAI-compatible gateway
  api_key: ${OPENAI_API_KEY}
  model: gpt-5-mini                        # any model your endpoint serves
```

Current public model ids that work well: `gpt-5-mini`, `gpt-5.4-mini`,
`deepseek-chat`, `claude-sonnet-4`, `gemini-2.5-flash` — use whatever your
provider routes (`/model` lists what your endpoint advertises).

Session flags: `mowi --continue` resumes your latest session,
`mowi --session <id>` resumes by id, `--no-stream` disables live token
streaming.

## First-run mental model

The screen is a **document**, not a chat log:

- **Header** — workspace, model, session state, and safety chips (write/shell
  permissions). On narrow terminals lower-priority chips drop first; safety
  state never does.
- **Transcript** — your prompts (soft-filled blocks), the assistant's answers
  (rendered markdown), and one compact tool line per turn. File edits appear
  as inline review cards with paired −/+ lines.
- **Activity band** — appears only while work runs: spinner, what the agent
  is doing in plain verbs ("searching · grep · loop.go"), and elapsed time.
- **Input** — type and press `enter`. While the agent is busy, new messages
  queue; `/steer <text>` interrupts the running turn instead.

The welcome splash dismisses itself on any key. Long sessions stay fast:
older transcript content is virtualized and re-rendered lazily as you scroll.

## Pack slash commands

Linked packs register slash commands the same way they register CLI
subcommands. With the stock `mowi` binary that includes `/review` and `/sec`
(from `packs/review`): same scope flags as `mow review` / `mow sec`, but they
run against **this session's model**. They do not start a `--reviewer`
ensemble. Exclusive — wait for the current turn to finish. See
[packs/review/README.md](../review/README.md).

## Trust & permissions

mowi runs tools on **your** machine. By default only read-only tools
(read, glob, grep) are enabled. Power tools — `write`, `edit`, `bash` — need
`--allow-write` / `--allow-shell`, and with the default *ask* mode each call
shows a permission strip with a real preview (the command, or a before/after
diff) and three answers:

```
y allow · n deny · a always (this session)
```

- `esc` always cancels the prompt; answers are ignored for a short window so a
  stray keystroke can't approve something you haven't read.
- `shift+tab` cycles ask ↔ auto; the header chip shows the current mode.
- `--auto` opts out of prompts entirely; `--ask` forces them.
- Workspace trust is managed out-of-band: `mowi trust` (never a file inside
  the repo itself).

## Essential keys & commands

| Key | Action |
|-----|--------|
| `enter` | send (queues while busy) |
| `ctrl+j` | newline |
| `ctrl+u` / `ctrl+d` | scroll transcript (wheel works too) |
| `esc` | cancel turn / dismiss |
| `ctrl+l` | clear transcript |
| `shift+tab` | permission mode auto ↔ ask |
| `ctrl+s` | select mode — release the mouse so the terminal can copy text |
| `ctrl+/` or `?` on empty input | help |
| `ctrl+c` | quit (cancels first when busy) |

Every binding is remappable (see Configuration). Commands, typed into the
input line:

| Command | What it does |
|---------|--------------|
| `/model` | interactive model picker (`/model <filter>` to jump) |
| `/effort` | reasoning effort: none/low/medium/high |
| `/sessions` | list resumable sessions for this workspace |
| `/search <term>` | find and cycle through matches in the transcript |
| `/copy`, `/retry`, `/edit` | copy last answer / retry / re-edit last prompt (also: arrow-up on empty input) |
| `/btw <q>` | aside answered without entering the main context |
| `/steer <text>` | redirect the running turn |
| `/status`, `/compact` | usage/context detail; compact history |
| `/goal`, `/lsp`, … | pack subcommands appear automatically when linked |

## Peers & sessions

mow can delegate to other agents (`acp_delegate` peers). While a peer works,
its stream shows as one quiet summary line (expanded live with `ctrl+p`), so
parallel peers never interleave into your transcript. Delegated token usage is
folded into the header total and broken out in `/status`.

Sessions are JSONL under `$MOW_HOME` keyed by workspace. `/sessions` shows
ids and previews with the relaunch command; in-app switching is deliberately
out — one Engine per process, so resume from the CLI.

## Configuration

mowi reads the `extensions.tui` section of the shared mow config:

```yaml
extensions:
  tui:
    welcome: true            # splash on/off
    welcome_message: |       # optional custom splash
      my own greeting
    prompt: "❯"              # input prefix
    theme:
      name: catppuccin-mocha # default, or any chroma style name
      colors:
        accent: "#FFD866"    # optional palette overrides
    keys:
      send: enter
      scroll_up: ctrl+u,pgup # comma-separated aliases
```

Themes derive the whole palette from a chroma style name; light/dark is
detected from the terminal. A malformed section falls back to defaults with a
warning rather than silently ignoring your config.

## Narrow terminals & accessibility

- Minimum size is **40×10**; below that mowi shows a centered size warning
  instead of a broken frame. Between 40 and ~100 columns, header chips and
  diff panels degrade gracefully (permission keys stay pinned and visible).
- `NO_COLOR=1` disables color; every status keeps a distinct glyph (◇ ⚙ ✕ ▲)
  so meaning survives without color.
- `MOW_NO_ANIM=1` stills the spinner (the elapsed clock keeps ticking — it is
  information, not decoration).
- `MOW_MOUSE=0` hands the mouse back to the terminal for native selection
  (`ctrl+s` does the same at runtime).
- mowi is a full-screen terminal app; it does not currently support
  screen-reader workflows. All operations are keyboard-complete.

## More

- Harness behavior (loop, tools, sessions): `docs/harness.md`
- Architecture and module layout: `docs/architecture.md`
- Extending with packs and extensions: `docs/extensions.md`
