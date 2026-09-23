# worktree

Dedicated git worktrees for parallel changes. Each worktree is its own
checkout on its own branch, so a second mow or mowi session can work there
while the main checkout keeps going, with no stash juggling and no two agents
editing the same files.

## Link

```go
import _ "github.com/subosito/mow/ext/worktree"
```

Stock `cmd/mow` blank-imports this package.

## Layout

For a repository at `/src/app`, worktree `fix-auth` lives at
`/src/app-worktrees/fix-auth` on branch `wt/fix-auth`. Worktrees sit beside the
repo, not inside it, so the main checkout's globs, greps and `git status` never
see them. Commands run from inside any worktree resolve back to the main
repository.

## Surfaces

| Surface | Name |
|---|---|
| CLI | `mow worktree list \| add NAME [--base REF] \| remove NAME [--force] \| path NAME` (`--dir` picks the repo) |
| Slash | `/worktree`, `/worktree add <name> [base]`, `/worktree remove <name> [--force]` |
| Tools | `worktree_list` (read-only), `worktree_add`, `worktree_remove` |

Start a parallel session in one:

```bash
mow worktree add fix-auth
mow tty --workspace "$(mow worktree path fix-auth)" --allow-write
# or, from mowi:
mowi --worktree fix-auth --allow-write
```

## Safety

- Names are `a-z`, `0-9`, `.`, `_`, `-` (max 64) and are used verbatim, never
  silently rewritten.
- `add` refuses an existing directory or branch; it never reuses someone's work.
- `remove` only touches worktrees this package created (under
  `<repo>-worktrees/` on a `wt/` branch). Without `--force` it refuses a
  worktree with uncommitted changes, or a branch with commits not merged into
  the main checkout's HEAD.
- `worktree_add` / `worktree_remove` create and delete directories outside the
  workspace jail, so they need `--allow-shell`. `worktree_list` is read-only.
- A session is jailed to its own workspace: an agent in the main checkout
  cannot edit a worktree's files, and vice versa. Open a session in the
  worktree to work there.

Merging stays a normal git operation (`git merge wt/fix-auth`) so you review
what lands. The goal pack's per-item worktree workers (`ext/goal`) are
separate, ephemeral, and merged automatically.

## Config

None. There is no `extensions.worktree` section.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/architecture.md](../../docs/architecture.md)
