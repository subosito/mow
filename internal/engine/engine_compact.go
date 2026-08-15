package engine

import (
	"fmt"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// CompactReport summarizes a manual compaction (Engine.Compact).
type CompactReport struct {
	// Layer is the compaction layer used ("snip" or "drop").
	Layer string `json:"layer,omitempty"`
	// CharsBefore/After are raw character estimates around the compaction.
	CharsBefore int `json:"chars_before,omitempty"`
	CharsAfter  int `json:"chars_after,omitempty"`
	// CharsSaved is the raw character reduction (before − after).
	CharsSaved int `json:"chars_saved,omitempty"`
	// MessagesBefore/After are the transcript sizes around the compaction.
	MessagesBefore int `json:"messages_before,omitempty"`
	MessagesAfter  int `json:"messages_after,omitempty"`
	// OverBudget is true when even drop+summarize could not reach the target.
	OverBudget bool `json:"over_budget,omitempty"`
}

// Compact manually compacts the engine's stored transcript (the context the
// next Prompt resumes with) using the same tiered machinery as the loop's
// automatic compaction: snip bulky tool results first, then drop+summarize
// old turns with task anchors. Raw session JSONL is never touched — this only
// rewrites the in-memory projection. maxChars <= 0 uses the default budget.
// Emits a loop.compact event. No-op (empty report) when there is no history.
func (e *Engine) Compact(maxChars int) (CompactReport, error) {
	if e == nil {
		return CompactReport{}, fmt.Errorf("engine: nil")
	}
	// The NEXT prompt's history is e.prior (the previous run's full message
	// list), NOT e.transcript (UI user/assistant turns only). Compacting the
	// wrong one made /compact a visual no-op on the wire — compact prior.
	e.mu.Lock()
	prior := append([]llm.Message(nil), e.prior...)
	configured := 0
	compactRatio := agent.DefaultCompactRatio
	toolLim := agent.DefaultMaxToolResultChars
	if e.cfg != nil {
		configured = e.cfg.Policy.MaxContextChars
		if e.cfg.Policy.CompactRatio > 0 {
			compactRatio = e.cfg.Policy.CompactRatio
		}
		if e.cfg.Policy.MaxToolResultChars > 0 {
			toolLim = e.cfg.Policy.MaxToolResultChars
		}
	}
	if e.opt.MaxContextChars > 0 {
		configured = e.opt.MaxContextChars
	}
	if e.opt.MaxToolResultChars > 0 {
		toolLim = e.opt.MaxToolResultChars
	}
	window := e.limitsLocked().ContextWindow
	// Calibrate chars/token from the last observed request so manual compact
	// matches the loop's density (code-heavy history tokenizes denser).
	//
	// lastProviderTokens is the provider's InputTokens: history *plus* the system
	// prompt, tool schemas, and the last user message. Dividing history chars
	// by that total inflates chars/token, which then under-estimates the
	// post-compact size — the header drops to near-zero and appears to
	// "rebound" on the next real measurement. Model the fixed overhead
	// explicitly instead, and add it back after converting history.
	charsPerToken := 0.0
	overheadTokens := 0
	if e.lastProviderTokens > 0 {
		if raw := agent.EstChars(prior); raw > 0 {
			// Assume the default density for the non-history part, so the
			// remainder is attributable to history.
			histTokens := int(float64(raw)/agent.DefaultCharsPerToken + 0.5)
			if over := e.lastProviderTokens - histTokens; over > 0 {
				overheadTokens = over
			}
			// Density from history alone, never from the padded total.
			if histTokens > 0 && overheadTokens > 0 {
				charsPerToken = float64(raw) / float64(e.lastProviderTokens-overheadTokens)
			} else {
				charsPerToken = float64(raw) / float64(e.lastProviderTokens)
			}
		}
	}
	e.mu.Unlock()
	if len(prior) == 0 {
		return CompactReport{}, nil
	}

	auto := maxChars <= 0
	if auto {
		maxChars = resolveMaxContextChars(configured, window, compactRatio)
		if maxChars <= 0 {
			// Manual compaction is an explicit request even when automatic
			// compaction was disabled with max_context_chars: -1.
			maxChars = agent.DefaultMaxContextChars
		}
	}
	// CompactTiered takes a *raw* char target. The loop converts the configured
	// budget through CompactTarget so density calibration matches applyCompact.
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = 4 // same default as agent.defaultCharsPerToken
	}
	targetRaw := agent.CompactTarget(maxChars, cpt)
	cur := agent.EstChars(prior)
	if auto && cur > 1 {
		// /compact must free real headroom even when history is still under the
		// soft ceiling (common with large gateway context_window scaling).
		// Keep ~20% of *non-system* history plus the system prompt (never
		// dropped). 20% of body is the sweet spot: far more room than the old
		// ~50% target, without the task-loss risk of 10%. Body-only measuring
		// avoids a target below the irreducible system size.
		sysChars := 0
		if len(prior) > 0 && prior[0].Role == "system" {
			sysChars = agent.EstChars(prior[:1])
		}
		body := cur - sysChars
		if body < 0 {
			body = 0
		}
		const keepBodyPct = 20 // of non-system history
		keepBody := body * keepBodyPct / 100
		const minBody = 2_000
		if keepBody < minBody && body > minBody {
			keepBody = minBody
		}
		keep := sysChars + keepBody
		if keep >= cur && body > 0 {
			keep = sysChars + body*keepBodyPct/100
		}
		if keep < 1 {
			keep = 1
		}
		if keep < targetRaw {
			targetRaw = keep
		}
	}
	if targetRaw < 1 {
		targetRaw = 1
	}
	res := agent.CompactTiered(prior, targetRaw, "", toolLim)
	if res.Messages == nil {
		return CompactReport{}, fmt.Errorf("engine: compact failed (nil result)")
	}
	// Best-effort archive of pre-compact history for recall.
	e.mu.Lock()
	sess := e.sess
	e.mu.Unlock()
	if sess != nil && (res.CharsSaved > 0 || res.Layer == "drop") {
		if _, aerr := sess.ArchiveCompact(prior, res.Layer, res.CharsSaved); aerr != nil {
			e.log().Warn("context archive", "err", aerr)
		}
	}
	e.mu.Lock()
	e.prior = res.Messages
	// Keep the UI transcript aligned with the compacted history (user/assistant
	// roles only, mirroring how run end appends to it).
	var t []Message
	for _, m := range res.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			t = append(t, toPublicMessage(m))
		}
	}
	e.transcript = t
	// Refresh the context-fullness estimate immediately. Hosts (e.g. mowi
	// header ctx%) read ContextTokens(), which otherwise stays at the pre-
	// compact LLM usage until the next provider call.
	//
	// Add back the fixed per-request overhead (system prompt, tool schemas):
	// compaction cannot remove it, so omitting it reports a context far
	// emptier than the next real request will measure.
	e.lastCtxEstimate = estimateCtxTokens(res.CharsAfter, charsPerToken) + overheadTokens
	e.mu.Unlock()

	rep := CompactReport{
		Layer:          string(res.Layer),
		CharsBefore:    res.CharsBefore,
		CharsAfter:     res.CharsAfter,
		CharsSaved:     res.CharsSaved,
		MessagesBefore: res.MessagesBefore,
		MessagesAfter:  res.MessagesAfter,
		OverBudget:     res.OverBudget,
	}
	e.Emit(Event{
		Type:           EventCompact,
		Layer:          CompactLayer(res.Layer),
		CharsBefore:    res.CharsBefore,
		CharsAfter:     res.CharsAfter,
		CharsSaved:     res.CharsSaved,
		MessagesBefore: res.MessagesBefore,
		MessagesAfter:  res.MessagesAfter,
		OverBudget:     res.OverBudget,
	})
	return rep, nil
}

// Rewind drops the most recent user↔assistant exchange from the live context
// (in-memory history + transcript) and returns that user prompt. Use it to
// implement retry/edit: after Rewind, re-Prompt the returned text (or an edited
// version) and it replaces the removed turn. The next Prompt writes a corrected
// full-history snapshot, so a later resume is consistent; the append-only
// session file keeps the superseded turn but LoadMessages uses only the last
// snapshot. Returns ("", false) when there is nothing to rewind.
func (e *Engine) Rewind() (lastUser string, ok bool) {
	if e == nil {
		return "", false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// prior: trailing messages back through the last REAL user prompt. Tool
	// results have role "tool", and host-injected nudges (thrash/explore
	// warnings, mid-turn steer) are marked Synthetic — skip both so edit/retry
	// lands on the user's own prompt, never a warning or steer string.
	i := len(e.prior) - 1
	for i >= 0 && (e.prior[i].Role != "user" || e.prior[i].Synthetic) {
		i--
	}
	if i < 0 {
		return "", false
	}
	lastUser = e.prior[i].Content
	e.prior = e.prior[:i]
	// transcript mirrors user/assistant turns only.
	j := len(e.transcript) - 1
	for j >= 0 && e.transcript[j].Role != "user" {
		j--
	}
	if j >= 0 {
		if lastUser == "" {
			lastUser = e.transcript[j].Content
		}
		e.transcript = e.transcript[:j]
	}
	e.lastProviderTokens = 0
	e.lastCtxEstimate = 0
	return lastUser, true
}
