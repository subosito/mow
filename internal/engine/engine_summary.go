package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// Compaction summarizer.
//
// The default compaction stub (agent.defaultCompactStub) is deterministic
// string assembly: it reports how many messages were dropped and which tools
// ran, but not what was *decided*. The model therefore loses the thread across
// a compaction and re-explores — re-reading files it already read, re-deriving
// conclusions it already reached. That re-exploration costs far more than one
// summary call, which is why this exists.
//
// It is off by default. The call is real money on a path that currently has
// none, and whether it pays depends on session shape: long single-task
// sessions win, short scattered ones may not. See eval/compaction for the
// measurement.

// summaryMaxChars bounds the generated summary. A summary that grows without
// limit defeats the purpose — it rides in context on every subsequent turn for
// the rest of the session, so it must stay far smaller than what it replaces.
const summaryMaxChars = 4_000

// summaryInputToolChars caps each tool result when serializing history for the
// summarizer. Tool bodies dominate a coding session's history and the
// summarizer needs their shape, not their bulk; without this the summary call
// itself becomes one of the most expensive requests of the run.
const summaryInputToolChars = 2_000

// summarySystemPrompt fixes the output shape. The sections are chosen for what
// a coding agent actually loses at a compaction boundary: the goal, the
// constraints it was told once, what is already done (so it does not redo it),
// and what it had decided (so it does not relitigate).
const summarySystemPrompt = `You are compacting the history of a coding session so work can continue after older turns are dropped.

Output ONLY the structured summary below. No preamble, no commentary, no code blocks.

## Goal
The user's objective in one or two sentences.

## Constraints
Requirements, preferences, and prohibitions the user stated. Include things said once and not repeated.

## Progress
- Done: what is finished and verified.
- In progress: what is underway and where it stands.
- Blocked: what is stuck and why.

## Key decisions
Choices made and the reason for each. These must survive: without them the work will be relitigated.

## Next steps
The immediate next actions.

## Critical context
Exact file paths, identifiers, commands, error strings, and values needed to continue. Be specific; a path or symbol name is worth more than a description of it.

Rules:
- Preserve specifics (paths, names, numbers) over generalities.
- Do not invent progress that is not in the history.
- If a section has nothing, write "none".`

// summaryUpdateSuffix is appended when a previous summary exists. Successive
// compactions otherwise decay: each summary summarizes the last, and detail is
// lost geometrically. Merging forward keeps early decisions alive.
const summaryUpdateSuffix = `

A summary of earlier history is included at the top of the conversation below.
PRESERVE every still-relevant fact from it and ADD what is new. Drop only what
later turns superseded. Do not compress the existing summary further.`

// summarizeHistory produces a structured compaction summary via one LLM call.
//
// Returns "" (not an error) whenever a summary cannot be produced: no client,
// nothing worth summarizing, or the call failed. Compaction must never fail
// because summarization did — the caller falls back to the deterministic stub.
func (e *Engine) summarizeHistory(ctx context.Context, ev agent.PreCompactEvent) string {
	e.mu.Lock()
	client := e.client
	e.mu.Unlock()
	if client == nil || len(ev.Messages) == 0 {
		return ""
	}

	body, prevSummary := renderHistoryForSummary(ev.Messages)
	if strings.TrimSpace(body) == "" {
		return ""
	}

	system := summarySystemPrompt
	if prevSummary {
		system += summaryUpdateSuffix
	}

	// OneShot: this prefix is sent once and never again, so writing a cache
	// entry for it is a pure surcharge (a cache write bills above plain
	// input). A fresh call also must not disturb the main conversation's
	// cached prefix.
	c := cloneLLMClient(client.OneShot())
	c.ExtraHeaders = cloneStringMap(c.ExtraHeaders)
	if c.ExtraHeaders == nil {
		c.ExtraHeaders = map[string]string{}
	}
	c.ExtraHeaders[llm.HeaderComponent] = "turn.compact"

	msg, err := c.Chat(ctx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: body},
	}, nil)
	if err != nil {
		e.log().Warn("compaction summary", "err", err)
		return ""
	}
	out := strings.TrimSpace(msg.Content)
	if out == "" {
		return ""
	}
	if len(out) > summaryMaxChars {
		out = out[:summaryMaxChars] + "\n…(summary truncated)"
	}
	e.Emit(Event{
		Type:         EventCompactSummary,
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	})
	return out
}

// renderHistoryForSummary serializes history for the summarizer and reports
// whether it already contains a prior compaction summary.
func renderHistoryForSummary(msgs []llm.Message) (string, bool) {
	var b strings.Builder
	prev := false
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		switch m.Role {
		case "system":
			// The system prompt is re-sent on every request anyway; including
			// it here would just pay for it twice.
			continue
		case "user":
			if content == "" {
				continue
			}
			if strings.Contains(content, compactStubMarker) {
				prev = true
				fmt.Fprintf(&b, "[Earlier summary]\n%s\n\n", content)
				continue
			}
			fmt.Fprintf(&b, "[User]\n%s\n\n", content)
		case "assistant":
			if content != "" {
				fmt.Fprintf(&b, "[Assistant]\n%s\n\n", content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "[Tool call] %s %s\n\n",
					tc.Function.Name, clip(tc.Function.Arguments, summaryInputToolChars))
			}
		case "tool":
			if content == "" {
				continue
			}
			fmt.Fprintf(&b, "[Tool result] %s\n%s\n\n",
				m.Name, clip(content, summaryInputToolChars))
		}
	}
	return b.String(), prev
}

// compactStubMarker identifies a prior compaction note in history.
const compactStubMarker = "[context compacted"

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("…(%d more bytes)", len(s)-max)
}
