# Embedding mow in Go

The library **is** the product. `mow run` and the packs are thin shells over
the same `mow.Engine` you get by importing the module. This page is the
how-to for that path: put an agent loop inside your own Go program, feed it a
custom HTTP client or LLM backend, add tools, watch events, and account for
tokens.

Stack context: [architecture.md](architecture.md). Loop/config internals:
[harness.md](harness.md). Packs: [extensions.md](extensions.md).

```go
import "github.com/subosito/mow"
```

Everything here is on the public `mow` package. You never import `internal/*`.

---

## 1. The smallest embed

```go
eng, err := mow.New(mow.Options{}) // reads $MOW_HOME/config.yaml + env
if err != nil {
    log.Fatal(err)
}
res, err := eng.Prompt(ctx, "List the Go files in this repo")
if err != nil {
    log.Fatal(err)
}
fmt.Println(res.Text)
```

`New` returns a live `*Engine`: config loaded, tools and hooks wired, an LLM
client built from `llm.base_url` + key + model (config or env). The engine is
**multi-turn** — call `Prompt` again and history carries:

```go
eng.Prompt(ctx, "Now write a test for the largest one")
```

One-shot without holding an engine:

```go
res, err := mow.Run(ctx, "Summarize this repo", mow.Options{})
```

`Run` is `New` + a single `Prompt`. Use it for CI and scripts; use `New` when
you want more than one turn, events, or model switching.

---

## 2. Options that matter for embedders

`Options` is how the host, not a config file, drives the engine. The fields
you reach for most:

| Field | Why |
|-------|-----|
| `HTTPClient *http.Client` | Route every LLM/media call through your transport — proxy, custom timeout, retry/log middleware. Nil → default client (120s chat, 180s media). |
| `Logger *slog.Logger` | Capture engine logs (`run`/`tool`/`warn`) without touching the process-global `slog.Default()`. Pass a discarding handler to silence. |
| `Provider Provider` | Swap the LLM backend entirely — see [§5](#5-a-custom-llm-backend). |
| `Tools []Tool` | Engine-scoped custom tools; two engines in one process can run different toolsets. See [§4](#4-custom-tools). |
| `Hooks Hooks` | Per-engine lifecycle callbacks (deny/rewrite tools, inject context). See [§6](#6-hooks). |
| `OnEvent EventFunc` | Structured lifecycle stream — the spine of any UI. See [§3](#3-events-streaming-and-token-usage). |
| `OnToken` / `OnReasoning` | Content and thinking deltas for live streaming UIs. |
| `AllowWrite` / `AllowShell` | Enable power tools from code instead of `--allow-*`. |
| `SessionID` / `Continue` / `NoSession` | Resume a specific session, resume the latest, or don't persist. |
| `Workspace` / `Model` / `BaseURL` | Point overrides that win over config/env. |
| `MaxTurns` | Loop budget: positive = N turns, `-1` = unlimited, `0` = leave config (default 120). |

`HTTPClient` and `Logger` are the two knobs an embedder almost always wants and
a config file can't give you:

```go
eng, err := mow.New(mow.Options{
    HTTPClient: &http.Client{
        Timeout:   90 * time.Second,
        Transport: myProxyTransport, // logging, metrics, egress proxy…
    },
    Logger: slog.New(slog.NewJSONHandler(logSink, nil)), // captured, not global
})
```

Because the logger is injected, a host running several engines can tag each
one's logs without racing on `slog.Default()`.

---

## 3. Events, streaming, and token usage

`OnEvent` gives you a typed lifecycle stream. It's how you'd drive a progress
UI, a metrics pipeline, or an audit log.

```go
eng, _ := mow.New(mow.Options{
    OnEvent: func(ev mow.Event) {
        switch ev.Type {
        case mow.EventRunStart:
            log.Printf("run %s: %q", ev.RunID, ev.Text)
        case mow.EventToolEnd:
            log.Printf("  %s (%dms)%s", ev.Tool, ev.DurationMs,
                map[bool]string{true: " DENIED"}[ev.Denied])
        case mow.EventRunEnd:
            log.Printf("run %s done: %s  in=%d out=%d",
                ev.RunID, ev.StopReason, ev.InputTokens, ev.OutputTokens)
        }
    },
})
```

Add more listeners any time with `eng.AddOnEvent(fn)`. Event types:
`run.start`, `token`, `reasoning`, `tool.start`, `tool.end`, `turn`,
`delegate.chunk`, `run.end`. Correlate across a process with `ev.RunID` (one
per `Prompt`) and `ev.SessionID`.

**Live token stream** (for a chat UI): set `Stream: true` plus `OnToken` (and
`OnReasoning` for thinking). Deltas arrive as they're produced; the loop still
strips inline `<think>` tags from committed history.

**Token accounting**: every LLM call's provider-reported usage is summed per
run — no tokenizer, the provider's own numbers.

```go
res, _ := eng.Prompt(ctx, "…")
fmt.Printf("in=%d out=%d\n", res.Usage.InputTokens, res.Usage.OutputTokens)
```

The same totals ride the `run.end` event (`InputTokens`/`OutputTokens`). Zero
means the provider reported none — not that nothing happened.

---

## 4. Custom tools

A tool is anything satisfying `mow.Tool`:

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage // JSON Schema object for the args
    Exec(ctx context.Context, args json.RawMessage) (string, error)
}
```

Pass tools per engine via `Options.Tools` — unlike the process-global
`ext.RegisterTool`, this scopes to one engine, so two engines in the same
process can expose different toolsets:

```go
eng, _ := mow.New(mow.Options{Tools: []mow.Tool{clockTool{}}})
```

Rules that keep you out of trouble:

- A per-engine tool **overrides** a registry tool of the same name.
- Colliding with a **builtin** name (`read`/`glob`/`grep`/`write`/`edit`/`bash`)
  is an error — the jailed builtins can't be replaced.
- Implement `ReadOnly() bool` returning `true` if the tool has no side effects,
  so it stays available in read-only prompts (ACP "ask" mode, `PromptOpts.ReadOnly`).

`Exec` returns the string the model sees. Return an `error` for a *hard*
failure that should abort the run; return a normal string describing the
problem (`"error: file not found"`) for a *soft* failure the model can recover
from — that's the convention the builtins follow.

---

## 5. A custom LLM backend

`Options.Chat` injects a bare function for tests, but it never streams. For a
real backend — a provider SDK, a router, an in-house gateway — implement
`Provider`:

```go
type Provider interface {
    Chat(ctx context.Context, messages []Message, tools []ToolSpec, hooks ChatHooks) (Message, error)
}
```

Streaming, tool calls, and usage all keep working: emit content through
`hooks.OnToken` (and thinking through `hooks.OnReasoning`) as you receive it,
and set `Usage` on the returned `Message` so `RunResult.Usage` and `run.end`
stay accurate.

```go
eng, _ := mow.New(mow.Options{Provider: myProvider{}})
```

To keep `Engine.ListModels` / `SetModel` functional under a custom provider,
also implement the optional extensions:

```go
type ModelLister interface  { ListModels(ctx context.Context) ([]ModelInfo, error) }
type ModelSwitcher interface { SetModel(id string) error }
```

`Provider` takes precedence over `Chat`. Prefer it for anything real; reach
for `Chat` only for quick fakes.

---

## 6. Hooks

Hooks are the lifecycle seams — enough to deny/rewrite tools, compress
results, and inject system text without a bespoke pack. Pass them per engine:

```go
eng, _ := mow.New(mow.Options{
    Hooks: mow.Hooks{
        OnPreTool: []mow.PreToolFunc{func(ctx context.Context, ev mow.PreToolEvent) (mow.PreToolDecision, error) {
            if ev.Name == "bash" && looksDangerous(ev.Args) {
                return mow.PreToolDecision{Deny: true, Message: "blocked by policy"}, nil
            }
            return mow.PreToolDecision{}, nil
        }},
    },
})
```

The `PreToolDecision` seam (the same one `cmdhook` bridges Claude Code plugins
onto):

| Field | Effect |
|-------|--------|
| `Deny` | Skip `Exec`; `Message` becomes the tool result the model sees. |
| `RewriteArgs` + `Args` | Replace the tool arguments before `Exec`. |
| `AdditionalContext` | Prepend text to the tool result the model sees. |

Order within a `Prompt`: `SessionStart` (once, in `New`) → `UserPrompt` →
[`PreCompact`?] → LLM → `AfterTurn` → per tool (`PreTool` → `Exec` →
`PostTool`) → `Stop`. Register more at runtime with `eng.AddPreTool` /
`eng.AddPostTool` / etc., or globally (all engines) with `ext.RegisterPreTool`
in an `init`. See [extensions.md § Hooks](extensions.md#hooks-lifecycle) for
the full table.

> When `policy.max_parallel_tools > 1`, `PreTool`/`PostTool` can run on several
> tools at once — keep hook bodies concurrency-safe.

---

## 7. Sessions

Sessions persist as JSONL under `session.dir` (default
`$MOW_HOME/sessions/<project-hash>/`). To build a session picker, list them:

```go
sessions, err := eng.Sessions() // newest first
for _, s := range sessions {
    fmt.Printf("%s  %s  %q\n", s.ID, s.Updated.Format(time.Kitchen), s.Preview)
}
```

`SessionInfo` is `{ID string; Updated time.Time; Preview string}`, where
`Preview` is the first user line. Resume one by id, or the latest, at
construction:

```go
eng, _ := mow.New(mow.Options{SessionID: picked}) // or Continue: true
```

`eng.SessionID()` reports the active id; `eng.Transcript()` returns the loaded
messages (useful to render a resumed conversation). Pass `NoSession: true` to
run without persistence (tests/CI).

---

## 8. Model and wire switching at runtime

```go
eng.Model()                       // active chat model id
eng.SetModel("gpt-4.1")           // switch for subsequent Prompts
eng.ListModels(ctx)               // GET /models (built-in client)
eng.Wire()                        // openai-chat-completions | openai-responses | anthropic-messages
eng.SetWire("anthropic-messages")
eng.SetModelWithWire(id, wire)    // when /models advertises a preferred wire
```

Under a custom `Provider`, `SetModel`/`ListModels` route to your
`ModelSwitcher`/`ModelLister` if implemented, otherwise return the current
model alone.

---

## 9. Cancellation

`Prompt` honours its `ctx`. For a UI cancel button, either cancel the context
you passed, or call `eng.Cancel()` — it fail-fasts mid tool-batch (siblings
cancelled), and whatever soft results already finished still append to history
in call order (`StopReason=cancelled`). The REPL wires this to Ctrl+C
per-turn; a TUI wires it to Esc.

---

## See also

- [architecture.md](architecture.md) — public vs internal, endpoint model
- [harness.md](harness.md) — loop, config, sessions, policy knobs
- [extensions.md](extensions.md) — packs, hooks table, ACP, media, cmdhook
