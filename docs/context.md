# Tool-result side-channel (design note)

**Status:** implemented — oversized tool results are stored per session at
produce time (optional pack `packs/contextsink`, linked by stock binaries;
disabled by default; 8 KiB inline cap when enabled) and recovered by the model via `context_search id=…`
(get-by-id, bounded window) or pattern search over stored files ("stored "
snippet headers).
**Audience:** maintainers extending the agent loop or the session store.
**Related:** [harness.md](harness.md) (loop, compaction, context archive, `context_search`), [embedding.md](embedding.md) (PostTool rewrite surface), [extensions.md](extensions.md) (packs/hooks).

---

## 1. Problem

Agent tool calls dump large blobs into the conversation: file reads, shell
output, grep hits, issue lists, logs. Every later turn re-sends that blob →
context fills → cost, latency, and quality degrade.

Today's control is blunt:

- `policy.max_tool_result_chars` (24_000) truncates each tool result for the
  model.

Truncation throws away the tail — permanently. The session archive only
captures what was in history at **compaction** time, so a truncated result's
tail never reaches any store. And until compaction happens, the (truncated)
result still rides in full on every turn.

The gap is not missing search or missing archive — it is that large results
are never captured **at the moment they are produced**.

## 2. What already exists (the spine)

mow already ships a session-scoped archive + search:

- **Session archive** — on compaction, pre-compact history is persisted as
  plain text under `<session-id>.archive/*.md` in the session dir, bounded
  (file cap, message cap, keep-count with pruning).
- **`context_search`** — read-only tool, fixed-string scan over the newest
  archive files, snippet output with stable `file:line` references, hard
  per-call and cumulative retrieval budgets, model never supplies a path.
- **Compact stubs** — the compaction stub advertises `context_search` when an
  archive exists, and stays silent when it cannot (sessionless runs).

The spine exists for history dropped by compaction. What is missing: the full
body of a large tool result is never stored at sink time — it is truncated
before it can be archived.

## 3. Direction

Make the loop store large tool results **when they are produced**, and show
the model a small stub instead of the blob. Enforcement is structural: mow
owns the loop, and every tool result passes host code (PostTool) before it
enters history — so compliance does not depend on the model choosing to
search, and no second process is needed.

1. **Sink (in the loop, core policy).** Every tool result above
   `max_inline_bytes` → full body persisted to the session store → live
   history gets a stub. Applies to *every* result entering the conversation,
   including pack, MCP, and delegated-peer results — not just built-in tools.
   Implemented as one more PostTool hook in the existing chain, so external
   plugins, cmdhook, permission gates, and event emitters keep working
   unchanged.

   **Hook ordering.** The sink rewrites the result to a stub; a PostTool
   hook registered *before* it sees the full text, one registered *after* it
   sees the stub. Plugins that need the raw result must run before the sink
   (or read it back from the store by id). Keep the sink last in the chain
   by default so downstream hooks are never surprised by a rewritten result.

2. **Store (per-session).** Extend the existing `<session>.archive/`
   mechanism: plain-text files, file/grep-based search, bounded and pruned.
   No new global store, no SQLite for the first cut.

3. **Retrieval (extend `context_search`).** Search already exists. Add
   bounded ranged retrieval by id/path. No new tool names, no MCP surface —
   the tool stays next to `read`/`bash` without prefix noise.

4. **Stub.** Terse and machine-readable: tool name, stable id/path, byte
   size, short head preview. Counts fully in the token budget. The 24k
   ceiling stays as a hard cap for anything that bypasses the sink.

5. **Config.** Knobs under `extensions.contextsink` (the optional pack that
   provides the write side of `context_search`):

   ```yaml
   extensions:
     contextsink:
       enabled: true           # required; default: off
       max_inline_bytes: 8000  # above this → store + stub
   ```

   **Off by default even when the pack is linked (stock binaries link it);
   opt in via `enabled: true`.** The sink replaces a lossy default —
   `max_tool_result_chars` truncation already destroys the tail permanently —
   with a lossless one, and savings are structural (no model compliance
   needed). It adds no new trust surface: the store lives under `$MOW_HOME`
   with session JSONL (same permissions), is bounded and pruned, and store
   failure falls back explicitly — never a dangling stub. Behavior when the
   store is unavailable: results the loop would keep whole (≤ 24k) stay
   inline untouched (nothing is lost); larger results become a bounded
   **head + tail** with an explicit omission marker, never silent truncation.

   **One migration caveat.** Only results produced *after* the upgrade become
   stubs; existing session JSONL is untouched. New session snapshots carry
   stubs (the full body lives in `<session>.tools/`, pruned at 64 files /
   32 MiB total, per-file cap 8 MiB — anything larger is never stored, so a
   stub can never point at an unretrievable body), so anything that parses
   session files for full tool output must either keep the `.tools/` store or
   omit it or set `enabled: false`.

Why not a sidecar: mow owns the tool pipeline, so structural enforcement is
free; a file-based archive plus grep search already ships. A second runtime,
an adapter layer, or soft “the model should search instead” routing would be
strictly worse here.

## 4. Decisions

**A. Pack, not core — with a core extension point.** The whole side channel
(write side + `context_search` recovery) ships as the optional
`packs/contextsink` pack: library embeds that never import it keep raw bodies
in history (no stubbing, no search tool, no new surface), while stock
binaries link it for the lossless default. What stays core is the *machinery*:
the session tool store, the session archive, the engine's generic ext hook and
tool registries (no context-specific slot), and the
`context_search` availability flag (`mow.SetContextSearchAvailable`) that lets
the compaction stub advertise recovery only when the tool actually exists.
The pack registers the search tool via `ext.RegisterTool`; it resolves the
session dir from the engine at call time (`EngineFromContext`), so no engine
wiring is needed. A pack-less embed can never end up with stubs it cannot
retrieve — nothing writes stubs — and a linked pack always runs last, after
every other hook has seen the full result.

**B. Per-session scope.** Store under the session's archive dir. Global or
workspace-scoped stores bleed between projects and make deletion semantics
unclear. One id space, one prune policy, and session deletion removes the
store with its session.

**C. Compaction stays cheap.** Stubs are stable; the archive is the durable
body. Index once at sink time — do not re-archive stub-only turns as if they
were full text. Compaction does **not** query the store: retrieval during
compaction adds hidden model/tool work and nondeterministic latency. Blob
references remain valid across process restarts; missing or expired blobs
fail with an explicit message.

**D. Peers gate at their own boundary.** Each Engine enforces the sink
independently; an external peer's returned payload is gated when it enters
the parent conversation. No shared store across peers.

**E. Literal search for the first cut.** Tool output is logs, paths, ids, stack
traces — exact match beats semantic heuristics. Whole-blob files first;
line-split chunks with fixed byte ceilings (optional overlap) only for huge
blobs, to improve hit locality. Keep stable blob/chunk ids so FTS5/BM25 can
be added later without changing the tool contract.

**F. Ranged retrieval.** get-by-id returns a bounded window (offset/range),
never the whole blob by default.

## 5. Security

- Stored context lives **outside** the workspace path jail → it needs its own
  boundary: quotas, retention, pruning, deletion with the session, and
  `0700`/`0600` permissions like the rest of `$MOW_HOME`.
- Tool output can carry secrets or hostile text. Per-session scope plus
  bounded retention limit the exfiltration surface. **Stub previews** redact
  common secret shapes; **`context_search` recovery is verbatim** (explicit
  product choice — recovery is opt-in and bounded, and the model needs
  faithful text when fetching by id or pattern). Session tool I/O uses
  `O_NOFOLLOW` on Unix; other platforms rely on Lstat containment plus
  post-open regular-file checks (residual TOCTOU on hostile hosts — see
  `internal/session/safefile*`).
- Store failure is a defined path, not an afterthought: the loop must still
  return a usable bounded result, with an explicit marker so the model knows
  text was dropped.
- Retrieval tools stay read-only; the search root is fixed by the engine; the
  model never supplies paths.

## 6. Open questions

1. Default `max_inline_bytes` — and does the sink *replace*
   `max_tool_result_chars` or sit under it as a separate ceiling?
2. Prune policy for sink files: reuse the archive's keep-count/size caps, or
   a separate per-session quota for stored tool bodies?
3. Do stubs get their own budget class in compaction accounting (they are
   much smaller than the blobs they replace)?
4. Does the `context_search` cumulative retrieval budget extend to get-by-id,
   or is a separate budget needed?
5. Chunking ceiling: whole-blob files vs line-split — decide once the first cut shows
   real blob sizes.

---

This builds on mow's own session archive and `context_search`
([harness.md](harness.md)), the PostTool rewrite surface
([embedding.md](embedding.md)), and the pack/extension rules
([extensions.md](extensions.md)) — no external product is part of the design.
