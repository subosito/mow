# review

Read-only two-pass code review (`review`) and adversarial security review (`sec`). Advisory: not a scanner, not a pentest, not proof the code is correct or secure.

## Link

```go
import _ "github.com/subosito/mow/packs/review"
```

Stock `cmd/mow` blank-imports this package. The Rust `mowi` sibling project
displays its RPC-driven results.

## Commands

| Surface | Name |
|---|---|
| CLI | `review`, `sec` (`mow review` / `mow sec`) |
| Slash | `/review`, `/sec` (`mow tty` and the Rust `mowi` TUI over `mow rpc`) |

No tools. Slash commands are exclusive (hosts refuse them mid-turn).

```bash
mow review                                  # uncommitted work (or whole tree if clean)
mow review --diff main...HEAD
mow review ./internal/api
mow sec --staged --fail-on high
mow sec --format sarif --output sec.sarif
```

Scope selectors are mutually exclusive: `--diff` → `--staged` → `--base` → `[paths…]` → dirty worktree → whole tree. Formats: `text` (default), `json`, `jsonl`, `sarif`. Exit: 0 clean · 1 findings at/above `--fail-on` · 2 error.

### CLI ensemble

Pass one can use several models. Repeat `--reviewer` or pass a comma-separated list; `--reviewers` is an alias of `--reviewer`. `--verifier` is **one** model (pass two; default: first reviewer). There is **no** `--verifier-model`. `--reviewer-parallel N` bounds concurrent candidates (0 = all). `--no-verify` cannot be combined with `--verifier`.

```bash
mow review --reviewer gpt-5-mini --reviewer claude-sonnet-4
mow sec --reviewers gpt-5-mini,claude-sonnet-4 --verifier gemini-2.5-flash
```

### Slash `/review` and `/sec`

They parse the same review flags as the CLI, but they run against **the session engine only**. They do **not** start an ensemble and they **ignore** `--reviewer` / `--reviewers` / `--verifier`. The model is the one the user is already talking to.

### Review jail

Write and shell are forced off. Each pass is `ReadOnly` + `Ephemeral` with `AllowedTools` limited to `read`, `glob`, and `grep`. Candidate/verifier engines use `SkipExtensionSetup` and `DisableExtensionHooks`. **ACP / `acp_delegate` is denied** (omitted from specs and denied at exec), along with MCP and other extension tools.

## Config (`extensions.review`)

Only budget caps. Personas and the two-pass workflow are not configurable.

```yaml
extensions:
  review:
    budgets:
      large:
        max_files: 200
        max_bytes: 2000000
        max_file_bytes: 200000
        max_turns: 90
```

Budget names: `small`, `medium`, `large`. Unset fields keep built-in values. Host/user config only (not project `.mow`).

## Docs

- [docs/review.md](../../docs/review.md) — workflow, schema, ensemble, jail
- [docs/extensions.md](../../docs/extensions.md) — slash vs CLI
- [docs/architecture.md](../../docs/architecture.md)
