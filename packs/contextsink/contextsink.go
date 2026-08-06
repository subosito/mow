// Package contextsink is the optional tool-result side channel: oversized
// tool results are stored beside the session and replaced in live history
// with a short stub, keeping the context window lean. Recovery is via the
// pack's own read side, the context_search tool (get-by-id, or pattern search
// over stored files).
//
// Blank-import this pack to enable (stock binaries do): the write side
// registers as an ordinary ext PostTool hook, the read side as an ext tool —
// no engine wiring or special core slot. Library embeds that never import
// this pack keep full tool results inline (no stubbing, no search tool).
//
// Config: extensions.contextsink (enabled, max_inline_bytes). The section is
// optional — defaults are disabled with an 8 KiB inline cap when enabled.
package contextsink

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

const (
	// defaultMaxInlineBytes is the result size below which the result stays
	// inline (seeded before the Extension overlay).
	defaultMaxInlineBytes = 8000
	// stubPreviewRunes bounds the model-visible preview in the stub.
	stubPreviewRunes = 400
	// fallbackHeadRunes / fallbackTailRunes bound the store-unavailable
	// fallback (head + tail with an explicit omission marker).
	fallbackHeadRunes = 4000
	fallbackTailRunes = 1000
	// loopToolResultCap mirrors the agent loop's default tool-result cap
	// (policy.max_tool_result_chars). On store failure, results at or under
	// this size are left inline (the loop keeps them whole); only larger
	// results get the explicit head+tail fallback, so a store outage never
	// shrinks the model's view below what the loop would have kept.
	loopToolResultCap = 24000
)

func init() {
	// Stock CLI and any host that blank-imports this package get the
	// tool-result side channel for free: the write side (store + stub) and the
	// read side (context_search) both register through the generic ext
	// hook/tool surface every pack uses — no engine wiring or special slot.
	// All stored data is session-scoped (see mow.Engine.SaveToolResult); only
	// the registrations are global.
	ext.RegisterPostTool(contextSinkHook)
	ext.RegisterTool(newContextSearchTool("", ""))
}

// config is extensions.contextsink.
type config struct {
	Enabled        bool `yaml:"enabled"`
	MaxInlineBytes int  `yaml:"max_inline_bytes"`
}

// loadConfig decodes extensions.contextsink. Missing section leaves defaults;
// decode errors leave defaults (a bad section must not break the run).
func loadConfig(eng *mow.Engine) config {
	cfg := config{MaxInlineBytes: defaultMaxInlineBytes}
	if eng != nil {
		_ = eng.Extension("contextsink", &cfg)
	}
	if cfg.MaxInlineBytes <= 0 {
		cfg.MaxInlineBytes = defaultMaxInlineBytes
	}
	return cfg
}

// contextSinkHook is an ordinary ext PostTool hook: oversized successful tool
// results are written to the session store and replaced with a short stub.
// It runs in ext registration order — the engine's event emitter runs before
// all hooks, so hosts still receive the full body on EventToolEnd even though
// history carries the stub. No-op when disabled, sessionless, denied,
// exec-error, under the cap, or for context_search itself (stubbing a
// recovery result would start a store→stub→recover loop).
func contextSinkHook(ctx context.Context, ev ext.PostToolEvent) (ext.PostToolDecision, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil || ev.Denied || ev.ExecErr != nil || ev.Name == "context_search" {
		return ext.PostToolDecision{}, nil
	}
	cfg := loadConfig(eng)
	if !cfg.Enabled || len(ev.Result) <= cfg.MaxInlineBytes {
		return ext.PostToolDecision{}, nil
	}
	id, err := eng.SaveToolResult(ev.Name, ev.Result)
	if err != nil {
		// Store unavailable or body over the store cap. Results the loop
		// would keep whole stay inline (no marker needed — nothing is lost);
		// only larger ones get the explicit head+tail fallback so the model
		// knows persistence did not happen and the tail is gone.
		if len(ev.Result) <= loopToolResultCap {
			return ext.PostToolDecision{}, nil
		}
		return ext.PostToolDecision{
			Result:  headTailFallback(ev.Result),
			Rewrite: true,
		}, nil
	}
	stub := formatStub(id, ev.Name, ev.Result)
	eng.Emit(mow.Event{
		Type:          mow.EventContextSinkStore,
		Tool:          ev.Name,
		ToolCallID:    ev.ToolCallID,
		StoredID:      id,
		OriginalBytes: len(ev.Result),
		InlineBytes:   len(stub),
	})
	return ext.PostToolDecision{
		Result:  stub,
		Rewrite: true,
	}, nil
}

// formatStub is the model-visible replacement for a stored tool body. Kept
// well under the history tool-result cap (~600 chars).
func formatStub(id, toolName, body string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "?"
	}
	preview := clampRunes(body, stubPreviewRunes)
	return fmt.Sprintf(
		"[stored id=%s tool=%s bytes=%d]\n%s\nuse context_search id=%s (or pattern= any literal above) to recover more",
		id, name, len(body), preview, id,
	)
}

// headTailFallback is the store-unavailable path: bounded head + tail with an
// explicit omission marker (never silent truncation).
func headTailFallback(s string) string {
	head, headBytes := firstNRunes(s, fallbackHeadRunes)
	tail, tailBytes := lastNRunes(s, fallbackTailRunes)
	if headBytes+tailBytes >= len(s) {
		// Fits in the head/tail windows (short multi-byte edge). Still mark
		// store failure so the model knows persistence did not happen.
		return head + "\n…(truncated: 0 bytes dropped; store unavailable)\n"
	}
	dropped := len(s) - headBytes - tailBytes
	return head + fmt.Sprintf("\n…(truncated: %d bytes dropped; store unavailable)\n", dropped) + tail
}

// firstNRunes returns the leading maxRunes of s and their byte length.
func firstNRunes(s string, maxRunes int) (string, int) {
	if maxRunes <= 0 || s == "" {
		return "", 0
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i], i
		}
		n++
	}
	return s, len(s)
}

// lastNRunes returns the trailing maxRunes of s and their byte length. Two
// passes avoid []rune allocation on multi-megabyte tool bodies.
func lastNRunes(s string, maxRunes int) (string, int) {
	if maxRunes <= 0 || s == "" {
		return "", 0
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s, len(s)
	}
	total := utf8.RuneCountInString(s)
	skip := total - maxRunes
	n := 0
	for i := range s {
		if n == skip {
			return s[i:], len(s) - i
		}
		n++
	}
	return s, len(s)
}
