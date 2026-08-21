package engine

import (
	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// ModelLimits describes the active model's context window and pricing.
// Prefer values published by GET /v1/models (gateway). Config may override.
// Zero fields mean unknown — hosts should not invent numbers.
type ModelLimits struct {
	ContextWindow int     // max context tokens (0 = unknown)
	InputPrice    float64 // USD per 1M input tokens (0 = unknown)
	OutputPrice   float64 // USD per 1M output tokens (0 = unknown)
	// CacheReadPrice is USD per 1M cached input tokens (0 = unknown). When the
	// gateway publishes it, cached input is priced at this rate instead of
	// InputPrice.
	CacheReadPrice float64
	// CacheWritePrice is USD per 1M tokens written into the prompt cache
	// (0 = unknown). Writes cost MORE than plain input (Anthropic ~1.25x),
	// so an unset value falls back to InputPrice and therefore understates.
	CacheWritePrice float64
}

// Cost returns the USD cost of this usage under l (0 if prices unknown).
//
// Input is split three ways because the three parts are billed at very
// different rates:
//
//   - cache read  — a deep discount (~0.1x input)
//   - cache write — a surcharge (~1.25x input)
//   - fresh       — the headline input rate
//
// This matters more than it looks. An agent loop re-sends a large stable
// prefix every turn, so on a caching provider most input is a read. But when
// the prefix is invalidated — model switch, compaction rewriting history, a
// change to the tool set — the next call re-inserts the whole conversation as
// a write. Measured on a real 406-turn session: reads were 83M tokens costing
// ~$125, writes were 33M tokens costing ~$612. Pricing writes as plain input
// understated that session's dominant line item by ~$122.
//
// Unknown cache rates fall back to InputPrice. For reads that is conservative
// (overstates); for writes it is not (understates), which is unavoidable
// without a published rate and is called out here so the direction is known.
func (u Usage) Cost(l ModelLimits) float64 {
	read, write := u.cacheSplit()
	fresh := u.InputTokens - read - write

	readRate := l.CacheReadPrice
	if readRate <= 0 {
		readRate = l.InputPrice
	}
	writeRate := l.CacheWritePrice
	if writeRate <= 0 {
		writeRate = l.InputPrice
	}
	return float64(fresh)/1e6*l.InputPrice +
		float64(read)/1e6*readRate +
		float64(write)/1e6*writeRate +
		float64(u.OutputTokens)/1e6*l.OutputPrice
}

// cacheSplit clamps the read and write shares so they never exceed the input
// total, keeping fresh non-negative. Gateways occasionally report shares that
// do not add up; a cost function must not emit a negative because of it.
func (u Usage) cacheSplit() (read, write int) {
	read, write = u.CachedInputTokens, u.CacheWriteInputTokens
	if read < 0 {
		read = 0
	}
	if write < 0 {
		write = 0
	}
	if read > u.InputTokens {
		read = u.InputTokens
	}
	if read+write > u.InputTokens {
		write = u.InputTokens - read
	}
	return read, write
}

// PromptCostEstimate is a pre-send approximation of the next Prompt's input size
// and USD cost so hosts can surface waste before the round trip.
type PromptCostEstimate struct {
	// InputTokens is approximate context size for the next call.
	InputTokens int
	// InputUSD is InputTokens priced at Limits.InputPrice (0 if price unknown).
	InputUSD float64
	// ContextWindow is Limits.ContextWindow (0 if unknown).
	ContextWindow int
	// FromProvider is true when InputTokens came from the last provider usage
	// report (ContextTokens); false when estimated from history char count.
	FromProvider bool
}

// EstimatePromptCost approximates the next Prompt's input token cost from
// current context. Prefers the last provider-reported ContextTokens; otherwise
// estimates from prior history at ~4 chars/token. System prompt and the new
// user message are not included in the history estimate (under-counts slightly
// on a cold first turn; fine for a pre-send warning).
func (e *Engine) EstimatePromptCost() PromptCostEstimate {
	if e == nil {
		return PromptCostEstimate{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lim := e.limitsLocked()
	out := PromptCostEstimate{ContextWindow: lim.ContextWindow}
	if e.lastProviderTokens > 0 {
		out.InputTokens = e.lastProviderTokens
		out.FromProvider = true
	} else if n := agent.EstChars(e.prior); n > 0 {
		out.InputTokens = estimateCtxTokens(n, agent.DefaultCharsPerToken)
	}
	if out.InputTokens > 0 && lim.InputPrice > 0 {
		out.InputUSD = float64(out.InputTokens) / 1e6 * lim.InputPrice
	}
	return out
}

// Limits returns context window and pricing for the active model.
//
// Priority (no speculative client catalog):
//  1. Explicit llm.context_window / llm.input_price / llm.output_price when set
//  2. Fields from the last successful GET /v1/models for this model id
//  3. Unknown (zeros)
func (e *Engine) Limits() ModelLimits {
	if e == nil {
		return ModelLimits{}
	}
	_ = e.ensureLLM()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.limitsLocked()
}

// limitsLocked is Limits while e.mu is already held. CatalogEntry is lock-free
// (map read on the live client); do not call Limits() under e.mu — deadlock.
func (e *Engine) limitsLocked() ModelLimits {
	model := ""
	var client *llm.Client
	if e.client != nil {
		client = e.client
		model = e.client.Model
	} else if e.cfg != nil {
		model = e.cfg.LLM.Model
	}
	var cfgCW int
	var cfgIP, cfgOP float64
	if e.cfg != nil {
		cfgCW, cfgIP, cfgOP = e.cfg.LLM.ContextWindow, e.cfg.LLM.InputPrice, e.cfg.LLM.OutputPrice
	}

	var l ModelLimits
	if client != nil {
		if info, ok := client.CatalogEntry(model); ok {
			l.ContextWindow = info.ContextWindow
			l.InputPrice = info.Pricing.InputPerMTok
			l.OutputPrice = info.Pricing.OutputPerMTok
			l.CacheReadPrice = info.Pricing.CacheReadPerMTok
			l.CacheWritePrice = info.Pricing.CacheWritePerMTok
		}
	}
	// Config overrides only when explicitly set (>0).
	if cfgCW > 0 {
		l.ContextWindow = cfgCW
	}
	if cfgIP > 0 {
		l.InputPrice = cfgIP
	}
	if cfgOP > 0 {
		l.OutputPrice = cfgOP
	}
	return l
}

// ContextTokens is the most recent LLM call's input tokens — an estimate of how
// full the context is right now. 0 before the first call or under a custom
// Provider that reports no usage. Updated on compaction (manual or automatic)
// so hosts can refresh a context-window indicator without waiting for the next
// provider round-trip.
func (e *Engine) ContextTokens() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastCtxEstimate > 0 {
		return e.lastCtxEstimate
	}
	return e.lastProviderTokens
}

// estimateCtxTokens converts a raw history char count into an approximate token
// count using the calibrated chars/token density (defaults to 4).
func estimateCtxTokens(charsAfter int, charsPerToken float64) int {
	if charsAfter <= 0 {
		return 0
	}
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = 4 // same default as agent.defaultCharsPerToken
	}
	// Unclamped density (1 char/token after a bad seed) reports history
	// *chars* as tokens — mowi then paints 1.4m/500k (271%). Keep the same
	// band the agent calibrator uses.
	if cpt < 2 {
		cpt = 2
	}
	if cpt > 8 {
		cpt = 8
	}
	tok := int(float64(charsAfter)/cpt + 0.5)
	if tok < 1 {
		tok = 1
	}
	return tok
}
