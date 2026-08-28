// Package focus is the explore-guard pack: soft heuristics that keep a run
// from burning turns re-reading, re-listing, and surveying instead of acting.
//
// These were compiled into the agent loop until they were moved here. They are
// workflow opinion, not engine mechanism: the engine's own guards (MaxTurns,
// context cancel, ErrStuck barren-batch detection) stay in core. Removing the
// blank import of this package disables every heuristic below and leaves no
// dangling reference in the loop.
//
// Behaviors (all soft — a run is never hard-killed by this pack):
//  1. degrade repeated views (read tool + bash cat/sed/head/tail of the same window)
//  2. degrade then refuse repeated inventory (git status/ls/find; git log/show/diff
//     keyed by args) and repeated grep/glob (distinct patterns do not collide)
//  3. block destructive git/rm that discards uncommitted work
//  4. treat test/build/commit bash as productive (resets the explore streak)
//  5. nag every turn after N consecutive explore-only turns
//  6. after a successful edit/write, allow one re-read of that path
//  7. cap unique-file reads this prompt (survey, not paging)
//
// Config: extensions.focus (explore_warn_every, reread_limit, survey_read_limit,
// inventory_limit, hard_inventory_limit, degraded_result_limit).
package focus

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"os"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/extcfg"
)

// hookSource tags this pack's hooks so a re-registration on a later BeforeNew
// replaces the previous generation instead of stacking duplicates.
const hookSource = "focus"

// state is captured per-Engine in the hook closures registered below.
// BeforeNew fires once per Engine construction, so each run gets a fresh
// streak/read/inventory ledger; engines built concurrently in one process
// never share a ledger (there is no process-global pointer).
func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return setup(configPaths...)
	})
}

func setup(configPaths ...string) error {
	var cfg Config
	// A malformed section must not abort Engine construction: the guards are
	// an advisory lane. Fall back to defaults and stay linked.
	if _, err := extcfg.DecodeSection("focus", configPaths, &cfg); err != nil {
		cfg = Config{}
	}

	// Workspace only unifies absolute vs relative spellings of the same path
	// in the re-read ledger. The process cwd is the run's workspace; if it is
	// unavailable the ledger simply falls back to literal path matching.
	ws, _ := os.Getwd()
	st := newFocusState(ws, cfg)

	ext.ClearHookSource(hookSource)
	register(st)
	return nil
}

// register installs the four seams this pack rides on.
func register(st *focusState) {
	// PreTool: bash guard (destructive / repeated inventory past the hard
	// limit) surfaces as Deny + Message. Repeated read/bash views only park
	// a Notice — the call still runs, PostTool caps the body.
	ext.RegisterPreToolSource(hookSource, func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
		return preTool(st, ctx, e.Name, e.Args, e.ToolCallID)
	})

	// PostTool: forget the path after a successful edit/write, apply the
	// degrade notice parked by PreTool, and annotate identical repeats.
	ext.RegisterPostToolSource(hookSource, func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
		out, rewrite := postTool(st, e.Name, e.Args, e.ToolCallID, e.Result, e.Denied, e.ExecErr)
		return ext.PostToolDecision{Rewrite: rewrite, Result: out}, nil
	})

	// AfterTurn (deciding form): the explore-streak nag.
	//
	// AfterTurnEvent deliberately does not carry the turn's tool calls, so the
	// streak is accumulated in the PreTool hook above (which sees every call
	// and already classifies explore vs productive) and merely evaluated here.
	ext.RegisterAfterTurnDecisionSource(hookSource, func(ctx context.Context, e ext.AfterTurnEvent) (ext.AfterTurnDecision, error) {
		if !st.closeTurn(e.HasToolCalls) {
			return ext.AfterTurnDecision{}, nil
		}
		return ext.AfterTurnDecision{Inject: exploreWarnMessage(st.streak())}, nil
	})
}

// closeTurn folds the tool calls observed since the last turn into the explore
// streak and reports whether the nag fires. Mirrors the old batchExploreOnly:
// a turn counts as explore-only when it made at least one tool call and every
// one of them was explore. Anything else (a productive bash, an edit/write, a
// custom tool, or no tools at all) resets the streak.
func (s *focusState) closeTurn(hadToolCalls bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sawAny, sawProductive := s.turnSawAny, s.turnSawProductive
	s.turnSawAny, s.turnSawProductive = false, false

	if !hadToolCalls || !sawAny || sawProductive {
		s.exploreStreak = 0
		return false
	}
	s.exploreStreak++
	return s.exploreStreak >= s.cfg.ExploreWarnEvery
}

// noteCall records one tool call's explore/productive classification for the
// turn in progress.
func (s *focusState) noteCall(name string, args json.RawMessage) {
	explore := isExploreToolName(name, args)
	s.mu.Lock()
	s.turnSawAny = true
	if !explore {
		s.turnSawProductive = true
	}
	s.mu.Unlock()
}

// denyText recovers the original stub from core's "error: " deny rendering.
func (s *focusState) denyText(result string) (string, bool) {
	const prefix = "error: "
	if !strings.HasPrefix(result, prefix) {
		return "", false
	}
	body := strings.TrimPrefix(result, prefix)
	// Only unwrap OUR stubs — another pack's deny must be left alone.
	if !strings.HasPrefix(body, "(") {
		return "", false
	}
	return body, true
}

// stashNotice parks a degrade notice between PreTool and PostTool for one call.
func (s *focusState) stashNotice(callID, notice string) {
	if callID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notices == nil {
		s.notices = make(map[string]string)
	}
	s.notices[callID] = notice
}

func (s *focusState) takeNotice(callID string) string {
	if callID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notices[callID]
	delete(s.notices, callID)
	return n
}

func (s *focusState) streak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exploreStreak
}

// truncate bounds the degraded body (same role as agent.TruncateToolResult).
// Rune-safe: never splits a multi-byte character at the cut.
func truncate(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	cut := maxChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… (truncated)"
}

var _ = json.RawMessage(nil)

// preTool is the PreTool body: read/grep/glob notices + bash guard.
func preTool(st *focusState, ctx context.Context, name string, args json.RawMessage, callID string) (ext.PreToolDecision, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	// Every call feeds the explore streak, including ones denied below:
	// a capped re-read is still an explore turn.
	st.noteCall(n, args)
	switch n {
	case "read":
		// Repetition alone does not refuse the call: it runs, and the
		// post-tool hook caps the body and prepends the notice.
		if notice := st.guardRead(args); notice != "" {
			st.stashNotice(callID, notice)
		}
	case "grep", "glob":
		guard := st.guardLookup(n, args)
		if guard.blocked() {
			return ext.PreToolDecision{Deny: true, Message: guard.Block}, nil
		}
		if guard.Notice != "" {
			st.stashNotice(callID, guard.Notice)
		}
	case "bash":
		guard := st.guardBash(args)
		if guard.blocked() {
			return ext.PreToolDecision{Deny: true, Message: guard.Block}, nil
		}
		// Repetition alone does not refuse the call: it runs, and the
		// post-tool hook caps the body and prepends guard.Notice.
		if guard.Notice != "" {
			st.stashNotice(callID, guard.Notice)
		}
	}
	return ext.PreToolDecision{}, nil
}

// postTool is the shared PostTool body. Returns the (possibly rewritten)
// result and whether it changed.
func postTool(st *focusState, name string, args json.RawMessage, callID, result string, denied bool, execErr error) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if denied {
		// Core renders a hook deny as "error: <msg>". Our stubs are not
		// errors — they are guidance — so restore the bare text.
		if msg, ok := st.denyText(result); ok {
			return msg, true
		}
		return result, false
	}
	if execErr == nil && (n == "edit" || n == "write") {
		st.forgetPath(toolArgString(args, "path"))
	}
	out := result
	if notice := st.takeNotice(callID); notice != "" {
		out = st.degradeToolResult(notice, out)
	}
	out = st.annotateRepeat(n, args, out)
	return out, out != result
}
