# mowi TUI: future-user review

This review combines independent audits by GLM and Qwen with a first-principles product synthesis. It is a discussion document, not an implementation plan.

## Executive recommendation

**Do not replace the current TUI shell yet. Modernize it through focused, evolutionary changes, while prototyping one optional “agent cockpit” layout for wide terminals.**

The current structure—header, transcript, activity, permission prompt, composer—is sound. It already has unusually good foundations: responsive header priorities, asynchronous rendering, readable diffs, permission gates, peer activity, narrow-terminal behavior, and performance/geometry tests. A visual rewrite would risk these strengths without fixing the largest problem.

The largest problem is not that mowi looks old. It is that a new user cannot quickly understand:

1. what mowi can do;
2. what it may change or execute;
3. how to discover actions and commands;
4. whether it is working, waiting, or stuck;
5. how peers, tools, goals, and sessions relate to the conversation.

A fresh palette alone would be cosmetic. The product needs a clearer mental model and better progressive disclosure.

## What a future user experiences today

A user arriving without prior mow knowledge sees a quiet chat-like terminal. That is approachable, but it hides the product’s differentiation. “Agentic coding” is generic language; peers, tool execution, approval boundaries, goals, and resumable sessions do not become obvious until encountered.

The current UI therefore has a mismatch:

- **The engine behaves like an agent workspace.**
- **The initial silhouette looks like a conventional AI chat.**

This is acceptable if mowi remains primarily for repository-aware power users. It becomes limiting if the future product is intended for cold acquisition, frequent multi-agent use, or long-lived project sessions.

## What should stay

- The basic vertical frame and transcript-first experience.
- The restrained, low-noise visual style.
- User/assistant distinction without oversized chat bubbles.
- Permission interruption as a deliberate trust boundary.
- Responsive removal of lower-priority header content.
- Collapsed peer output as the default; expanded output should remain intentional.
- Async rendering, transcript virtualization, and the current performance budget.
- Rich diff rendering and semantic tool/activity labels.
- TUI isolation inside `packs/mowi`.

These are product strengths, not legacy constraints to erase.

## Highest-priority improvements

### P0 — Make the first five minutes understandable

1. **Improve the first-run welcome.**
   Explain in plain language that mowi can inspect a repository, propose or make changes, run commands when allowed, and ask before protected actions. Include two or three useful example prompts.

2. **Keep a useful empty state after the splash is dismissed.**
   Avoid a blank viewport. Show faint examples such as “Explain this repository,” “Find why this test fails,” and “Plan a safe refactor.”

3. **Add first-class mowi documentation.**
   A `packs/mowi/README.md` should explain installation, the trust model, core keys, sessions, peers, and screenshots or terminal captures.

4. **Make permission previews readable.**
   The current one-row form is efficient but can flatten complex commands or file operations. Add an expand/details action without weakening the immediate approve/deny flow.

5. **Allow help while work is in progress.**
   A user is most likely to need help when something unfamiliar is happening.

### P1 — Make capabilities discoverable

6. **Add slash-command completion.**
   Typing `/` should open a filtered list with short descriptions; Tab completes and Enter selects. This is more valuable than expecting users to memorize a help card.

7. **Turn help into progressive disclosure.**
   The first page should contain essentials. Advanced commands, permissions, peers, and customization can be expanded. Replace internal vocabulary with user language, for example:
   - “peer” → “delegated agent” on first mention;
   - “aside—not added to context” → “quick question that does not change the conversation”;
   - “steer” → “send guidance to the running task.”

8. **Add typo suggestions for commands.**
   `/modle` should suggest `/model`.

9. **Expose focus and navigation state.**
   Transcript focus should not be invisible. Add turn-level navigation so long sessions are traversable without line-by-line scrolling.

10. **Make sessions browsable rather than merely printable.**
    A filterable picker is a better future-facing model for resumable work.

### P2 — Improve information architecture without adding noise

11. **Clarify activity phases.**
    Distinguish planning, generating, waiting for approval, running a tool, and waiting on delegated agents. Do not expose hidden chain-of-thought; expose operational state.

12. **Declutter narrow headers.**
    Below a width threshold, shorten the wordmark and hide non-default effort metadata before sacrificing workspace, safety posture, or context pressure.

13. **Add a discoverable command palette.**
    A `Ctrl+K` palette could unify commands, sessions, model changes, theme changes, and extension-contributed actions.

14. **Offer accessibility presets.**
    High-contrast and reduced-color themes are more useful than a visual rebrand. Important state must never depend on color alone.

## Does it need a fresh layout?

### The case against

A replacement layout would spend engineering effort on geometry, rendering, and regressions while onboarding and discoverability remain unresolved. The existing frame works at 80×24 and already adapts under pressure. The theme is quiet rather than obsolete.

### The case for

A fresh spatial model becomes justified if mowi’s future identity is explicitly **“agent cockpit, not chat.”** In that product:

- delegated agents are active frequently, not occasionally;
- parallel tools and task status are headline features;
- sessions are long-lived workspaces;
- users need persistent orientation across a large transcript;
- trust and approvals are central differentiators.

An interleaved transcript cannot always communicate five simultaneous agents cleanly. If this usage becomes common, a persistent activity rail can make the product’s differentiation visible rather than buried in transcript entries.

### Reconciliation

Do not choose between “old layout” and “complete redesign.” Keep the current layout as the universal default and prototype an **adaptive wide-terminal mode**. If it proves useful, it can become an optional layout and later earn default status through evidence.

## Proposed layouts

### Standard layout — all normal terminals

```text
 mowi · workspace · model                 read only · context 42%
──────────────────────────────────────────────────────────────────

 You
 Explain how authentication works and identify its main risks.

 Assistant
 The request passes through …

  read  internal/auth/middleware.go
  read  internal/auth/session.go

──────────────────────────────────────────────────────────────────
 planning · 8s · 2 tools
> Ask anything…                                      ? for help
```

When approval is required, the permission strip should interrupt directly above the composer and offer a details/expand key.

### Compact layout — around 80×24 or narrower

```text
 ◇ workspace                         read only · 42%
─────────────────────────────────────────────────────
 You: explain authentication risks

 The request passes through …

 running test · 8s
 approve shell command?                 y yes  n no
> _
```

Only identity, safety posture, context pressure, immediate activity, and required action survive. Model/effort details move to a picker or help surface.

### Experimental cockpit — wide terminals only

```text
 mowi · workspace · model                         read only · 42%
───────────────────────────────────────┬──────────────────────────
                                       │ ACTIVITY
 Conversation                          │ host    reviewing changes
                                       │ agent 1 testing auth
 You                                   │ agent 2 reading docs
 Explain authentication risks.         │
                                       │ CHANGES
 Assistant                             │ 3 files · +41 −12
 The request passes through …          │
                                       │ PERMISSIONS
                                       │ no request pending
───────────────────────────────────────┴──────────────────────────
> Ask, steer, or run a command…
```

The rail should appear only when the terminal is wide enough and when it contains meaningful activity. It must collapse cleanly into the standard transcript model.

## Visual style recommendation

Keep the restrained base style, but make hierarchy more deliberate:

- one accent for interactive/focused elements;
- semantic colors for success, warning, failure, and permission state;
- stronger spacing and rules before introducing more boxes;
- consistent labels and verbs for tool states;
- no decorative glyph proliferation;
- optional high-contrast and reduced-color presets;
- theme picker with preview, rather than requiring configuration edits.

The desired result is **fresh through clarity**, not fresh through ornament.

## Prototype before committing to a redesign

Build a disposable or config-gated wide activity rail and test it against the existing layout using three scripted scenarios:

1. a simple question with no tools;
2. a coding task with several tool calls and one approval;
3. a multi-agent task with parallel activity and a long transcript.

Evaluate:

- Can a first-time user state what is happening and what action is required?
- Is important information found faster than in the current transcript?
- Does the rail become dead space during ordinary use?
- Does it work meaningfully at 80×24, or degrade cleanly there?
- Does it preserve render/update performance and PTY cell geometry?
- Does it reduce confusion without increasing persistent noise?

Adopt a new default only if frequent real-world sessions benefit. A reasonable signal is that peer or parallel-tool activity occurs in roughly 30% or more of turns; below that, persistent chrome is likely wasteful.

## Suggested roadmap

| Phase | Work |
|---|---|
| P0 | README, first-run trust/value copy, empty state, busy help, expandable permission preview |
| P1 | slash completion, progressive help, typo suggestions, turn navigation, visible focus |
| P2 | session/command picker, operational phases, accessibility presets, header cleanup |
| Experiment | config-gated wide activity rail tested with scripted PTY scenarios |
| Later | consider making cockpit mode default only from observed usage and usability evidence |

## Main implementation risks

- Every added chrome row affects exact viewport accounting.
- New overlays can conflict with help, model, effort, session, and permission surfaces.
- Persistent status rendering can regress the update-time budget.
- Wide-only designs may look impressive in screenshots but fail the normal laptop-terminal experience.
- Always-visible trust state can become wallpaper; approval should still interrupt at the decision point.
- A command palette must consume extension-registered commands rather than create a second registry.
- Changes to glyph geometry need PTY smoke tests, not only string-based unit tests.

## Bottom line

mowi does not need a visual reset to feel current. It needs to reveal what it already is: a capable, observable, permission-aware agent workspace. Improve onboarding, command discovery, navigation, and operational clarity first. In parallel, prototype a wide “cockpit” mode to test whether future multi-agent use truly deserves a new spatial layout.

A more detailed independent axis-by-axis audit is available in `notes/mowi-product-audit.md`.
