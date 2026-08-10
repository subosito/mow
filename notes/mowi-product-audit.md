# mowi product audit — terminal product for a first-time user

Scope: `packs/mowi/` as a standalone interactive product. Audit only — no code
changes. Sources cited per finding.

## What exists today (mental-model baseline)

Frame (`tui_chrome.go` → `mainFrame`, `layoutChrome`):

```
header (2 rows: identity + rule)
transcript viewport
[activity band: 2 rows, only when busy/queued/perm]
[permission strip: 1 row, only when prompting]
input (rule + textarea, grows to cap 12)
```

- Transcript is a *document*, not chat bubbles: 3-cell role gutter
  (`styles.go roleGutterW`), user prompts get a soft fill + inline timestamp
  (`tui_transcript.go userBlock`), assistant markdown is glamour-rendered
  **asynchronously** (`tui_stream.go`, `kickEntryPretty`), tools fold into one
  per-turn tally line (`renderToolTallyLine`), write/edit results become review
  cards with paired −/+ rows (`render.go renderPrettyDiff`).
- Live work lives on the ephemeral activity band: spinner + phase ticker
  (seconds since last activity) left, elapsed + peers + queue right
  (`renderActivityBand`), with semantic verbs ("searching · grep · loop.go",
  `label.go activityToolLabel`).
- Trust surface: permission strip with `y allow · n deny · a always`, a 280 ms
  arm window so a mid-type "y" can't approve an unread shell
  (`tui_perm.go permDecisionArmed`), `shift+tab` ask↔auto cycle, safety chips
  in the header.
- Performance machinery is a first-class design constraint: history cache,
  virtualized pretty window (`virtual.go`), single-flight live glamour,
  100 ms heartbeat, entry-text GC (`gc.go`), and a freeze-test budget of
  25 ms per busy `Update` (`freeze_test.go`).
- Extensibility: `extensions.tui` config (welcome, prompt, theme, keys —
  `config.go`), pack-registered slash commands surface in help automatically
  (`helpCard`, `packslash.go`).

## Axis-by-axis findings

### 1. Onboarding — weakest axis
- **There is no README in `packs/mowi/`** (only `go.mod`/`go.sum`). A new user
  meets the product via `mowi --help` (`cmd/mowi/main.go`) and the splash.
- Splash (`welcomeView`) is quiet and honest — wordmark, "agentic coding in
  your terminal", model id, one hint line — but it teaches *nothing* about
  what makes mowi different: tools run on your machine, files get edited,
  permission prompts will appear, sessions persist. First-turn surprise ("it
  just edited a file") is the biggest trust risk for a new user and nothing
  pre-frames it.
- One-shot teach moments exist (queue vs `/steer`, select mode, ctx pressure)
  — good pattern, but they fire as `kindStatus` bullets inside the transcript
  and scroll away.
- `--continue`/`--session` resume is CLI-only; `/sessions` prints a table but
  switching is out-of-process. A first user who quits won't discover how to
  get back except via help text.

### 2. Mental model & navigation
- Document metaphor is consistent and calm; the per-turn tool tally is a smart
  anti-noise choice. But there is **no turn-level navigation**: scrolling is
  viewport-only (`ctrl+u/d`, wheel), and finding "where my last prompt was" in
  a long session means `/search` or eyeballing user fills.
- `ctrl+o` transcript-focus mode exists (`tui_ux.go toggleFocus`) but is
  invisible: nothing indicates which pane is focused beyond dropped keystrokes,
  and accidentally entering it (typing that goes nowhere) reads as a freeze.
- `followBottom` + "↓ new output · end/pgdn to follow" overlay
  (`mainFrame`/`overlayViewportFooter`) is well done — a real strength.
- `/sessions`, `/model`, `/effort`, `/lsp`, `/goal`, `/btw`, `/steer`,
  `/search`, `/copy`, `/retry`, `/edit`, `/status`, `/compact` — a rich verb
  set, discoverable only through the help card or amber-tinted typing.

### 3. Information hierarchy
- Strong: one glyph vocabulary (`styles.go` — ◇ ⚙ ✕ ▋ → · ▲ ◈), errors never
  dimmed, status deliberately *not* faint (contrast rule enforced by tests),
  diff glyphs survive NO_COLOR/color-blindness by design (`render.go` gutter
  comment).
- Weakness: the header carries identity (wordmark·model·cwd) **and** safety
  chips **and** token totals **and** ctx gauge on one row, with priority-drop
  under width pressure. That is four jobs in 24pt of chrome. The ctx gauge
  already suppresses itself below width 100 (`ctxGaugeMinWidth`) — a sign the
  row is full.
- Token usage is honest ("reported this run", `/status` breakdown) but the
  header total is a single number with no in/out split; fine as-is.

### 4. Conversation/tool activity
- The activity band is the best piece of the product: semantic verbs, phase
  ticker separate from total elapsed, stall note via `lastActivityAt`, peer
  labels with a protected minimum width (`minPeerLabelWidth`), stack-aware perm
  count "(1 of 3)".
- Peer output defaults to collapsed one-liners (`ctrl+p` to expand) — correct
  default for flicker/selection reasons (`peer_live.go` comments), but the
  toggle is undiscoverable until needed.
- Thinking is indicator-only (timer, no body) — a deliberate restraint; keep it.

### 5. Trust & safety
- Best-in-class details: perm arm window, real command/diff previews instead of
  JSON (`permPreview`), "a" scoped to *session*, header safety chips retained
  at narrow widths (`TestHeaderNarrowKeepsSafety`).
- Gaps: the perm strip flattens multi-line previews to one line with `⏎`
  (`renderPermissionStrip`) — a 14-line write preview becomes unreadable
  exactly when you most need to read it. No scroll/expand affordance for the
  preview (args capped at 4000 cells in `Update`).
- `a always` is one keystroke with no undo cue beyond a status line; a visible
  chip does show auto afterwards, which is good.

### 6. Keyboard discoverability
- Help card (`helpCard`) is grouped (KEYS/COMMANDS), registry-driven, and uses
  resolved keys — excellent. But **help only opens when not busy**
  (`tui_update.go`: `if !m.busy && m.modelPick == nil`) — the moment you want
  to check a key during a long turn is the moment it's refused.
- No tab-completion for slash commands; no palette. Keys are fully
  remappable (`KeysConfig`) but there is no way to see current bindings
  outside the overlay.
- Binding risks: `ctrl+s` select mode collides with terminal/save muscle
  memory; `ctrl+/` help is unmapped on several terminals/layouts. Both have
  aliases (`?` when empty), which mitigates.

### 7. Visual design
- Quiet, consistent, testable: theme derives a full palette from any chroma
  style name, light/dark via OSC 11 probe *before* Bubble Tea owns the tty
  (`pinTerminalTheme`), code blocks palette-derived by default with opt-in
  chroma. This is a coherent house style; a redesign would be aesthetic churn.

### 8. Narrow terminals
- Handled seriously: hard 40×10 floor with a centered size warning
  (`tooSmall`/`sizeWarnView`), header priority-drop, perm keys pinned right
  with the preview yielding, diff collapse at 40 lines, gutter sized to real
  line numbers (`newDiffGutter`). Among the better narrow-tty stories in TUIs.
- Remaining hazard: at 40–60 cols the header chip drop order is the only
  defense; safety state must never drop (tested), but ctx%/tokens/model may
  vanish silently — acceptable, document it.

### 9. Accessibility
- Real provisions: `NO_COLOR` respected, `MOW_NO_ANIM` stills the spinner but
  keeps the elapsed clock ("information, not decoration"), glyph+color pairing
  everywhere, full keyboard operation.
- Gaps: no high-contrast theme preset; transcript-focus mode has no focus
  indicator (a screen-reader-adjacent orientation problem even for sighted
  users); nothing documents screen-reader incompatibility of alt-screen TUIs
  (should be stated honestly in docs).

### 10. Performance perception
- Strongest axis after safety. Async glamour, stable-prefix streaming,
  history cache, virtualization, GC, 25 ms freeze budget, resize debounce
  (`tui_ux.go resizeSettleDelay`). Perceived latency is addressed by the
  always-on elapsed ticker. Do not trade this away for richer chrome.

### 11. Extensibility
- `extensions.tui` (welcome/prompt/theme/keys) + registry-driven slash/help is
  the right shape. Missing: layout/density options (focus mode, chrome level),
  which blocks host-specific tuning without forking the render path.

## Verdict: evolutionary refinement, not a fresh layout

A rewrite would discard: the 25 ms-per-frame budget and its regression tests,
the geometry tests that only `smoke-tui` cell-grid asserts catch (see AGENTS.md
on the diff-sign 6-column bug), the narrow-width drop logic, and a keymap that
is already configurable. The layout's skeleton (header | transcript | band |
perm | input) is sound; the problems are onboarding, discoverability, and two
interaction gaps (turn navigation, readable perm previews) — none of which
need a new frame.

## Proposed changes (concrete, no rewrite)

### Layout variants (all behind `extensions.tui`, default unchanged)
1. **Zen/focus chrome** (`chrome: focus`): hide header and rule until state
   changes (perm prompt, error, mode toggle) or a key press (`ctrl+.`
   chrome-toggle). Gives the transcript +2 rows — matters most at 80×24
   laptops. Implementation lives entirely in `layoutChrome`/`mainFrame`.
2. **Bottom status-bar variant** (`chrome: bar`): merge header + activity band
   into one persistent bottom row (tmux-style) directly above the input, where
   the eye already lives during streaming. Tradeoff: identity row disappears;
   the 25 ms budget must absorb one always-on band render (it can — the band
   is cheap when idle). Offer, don't switch the default.
3. **Wide rail** (auto at ≥120 cols, opt-in): a right rail (28–36 cols) with
   expanded peer bodies + tool log + ctx gauge, replacing `ctrl+p` inline
   expansion. Transcript keeps ≥80 cols or the rail refuses to open. This is
   the honest answer to "where do I put peers" instead of interleaving.

### Interaction patterns
- **Turn navigation**: `ctrl+up` / `ctrl+down` jump between user blocks
  (entry indices are already tracked for `/search` — `searchHits` machinery in
  `tui_commands.go` generalizes to kind-filtered jumping).
- **Perm preview expansion**: `v` (or `tab`) while `permWait != nil` swaps the
  one-line strip preview for a bounded overlay of the full pretty diff/command
  (overlay infra `overlay.go` already exists). Fixes the unreadable-⏎ problem.
- **Command palette**: `ctrl+k` opens a filterable list over the slash
  registry (same overlay machinery as model/effort pickers); also solves
  discoverability of `/btw`, `/steer`, `/copy`, `/retry`.
- **Tab completion** in the textarea for `/…` prefixes against the registry.
- **Focus indicator**: when `focus == focusTranscript`, tint the header rule
  or show a `[scroll]` chip — never silent mode.
- **Session picker overlay**: turn `sessionsTable` output into a picker
  overlay that prints the exact relaunch command per row (in-app switching is
  out — one Engine per process — but make the copy-paste one keystroke).

### Onboarding
- **README in `packs/mowi/`**: what it is, the trust model in 5 lines,
  keymap table, `extensions.tui` reference, accessibility statement
  (NO_COLOR/MOW_NO_ANIM/MOW_MOUSE; alt-screen limitation).
- **First-run welcome upgrade**: keep the quiet splash, add three faint example
  prompts and one safety line ("file/shell actions ask before running —
  shift+tab toggles"). Configurable via existing `welcome_message`.
- **Teach moments → status-bar-ish persistence**: the one-shot statuses
  (queue, select mode) should repeat in the help card text, not only fire once.

## Prioritized roadmap

**P0 — trust & orientation (cheap, high leverage)**
1. Write `packs/mowi/README.md` (onboarding, keymap, trust model, a11y notes).
2. Allow opening help while busy (`tui_update.go` guard is one condition).
3. Perm preview expand key + keep full preview readable (`tui_perm.go`,
   `overlay.go`).
4. First-run splash additions + safety line.

**P1 — navigation & discoverability**
5. Turn jump keys (ctrl+up/down over user entries).
6. Tab completion for slash commands; `ctrl+k` palette overlay.
7. Focus-mode indicator for transcript focus.

**P2 — layout options (config-gated, default unchanged)**
8. `chrome: focus` zen mode via `layoutChrome`.
9. Wide rail for peers/tools at ≥120 cols.
10. Session picker overlay.

**P3 — polish**
11. `chrome: bar` bottom status-bar variant (needs perf re-verification under
    `freeze_test.go`).
12. High-contrast theme preset; contrast audit of muted-on-fill combos.
13. Docs: narrow-terminal drop behavior + screen-reader statement.

## Risks & tradeoffs

- **Chrome changes vs. layout budget**: `layoutChrome` must stay an exact
  accounting of rendered rows; every variant needs `layout_test.go` fixtures
  and `smoke-tui` cell-grid verification (geometry bugs pass unit tests — the
  diff-sign incident).
- **Overlay stacking**: help/model/effort already compete in `View()`; adding
  palette + perm-preview + session picker needs an explicit overlay priority
  and z-order tests.
- **Perf**: any always-on chrome (bottom bar) re-enters the busy `Update`
  path; must stay under the 25 ms budget on a flooded stream (`freeze_test.go`
  is the gate).
- **Config surface**: new `extensions.tui` keys are additive-only; renaming
  existing keys breaks user configs (they resolve via `Resolve()` — keep
  defaults stable).
- **Module boundary**: nothing here may pull TUI deps into root/`internal`
  (AGENTS.md); the wide rail and palette are pure `packs/mowi` work.
- **Default-change temptation**: the collapsed-peer default and single-spinner
  rule exist because of real flicker/selection bugs (`peer_live.go`,
  `TestOneSpinner*`); proposals above add opt-ins rather than flipping them.
