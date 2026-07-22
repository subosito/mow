# Contributing to mow

Build, test, layout, conventions, and security invariants all live in
[AGENTS.md](AGENTS.md) — it is written for AI coding agents but is the
contributor guide for humans too.

Short version:

```bash
devenv shell -- just verify   # go test ./... + go vet — gate before commit
# or with plain Go:
go test ./... && go vet ./...
```

- **Commits must use Conventional Commits** — see [AGENTS.md § Commits](AGENTS.md#commits)
  (`feat(scope): …`, `fix: …`, `docs: …`, `chore: …`). No informal subjects.
- Match surrounding style; scoped diffs; no drive-by refactors.
- Test non-trivial logic, table-driven like the nearest `*_test.go`.
- Do not regress the security invariants listed in AGENTS.md.
