package mow

import "github.com/subosito/mow/internal/llm"

// ModelLimits describes the active model's context window and pricing.
// Prefer values published by GET /v1/models (gateway). Config may override.
// Zero fields mean unknown — hosts should not invent numbers.
type ModelLimits struct {
	ContextWindow int     // max context tokens (0 = unknown)
	InputPrice    float64 // USD per 1M input tokens (0 = unknown)
	OutputPrice   float64 // USD per 1M output tokens (0 = unknown)
}

// Cost returns the USD cost of this usage under l (0 if prices unknown).
// Cache discounts are not applied (gateway may still bill less for cache hits).
func (u Usage) Cost(l ModelLimits) float64 {
	return float64(u.InputTokens)/1e6*l.InputPrice + float64(u.OutputTokens)/1e6*l.OutputPrice
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
	e.mu.Lock()
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
	e.mu.Unlock()

	var l ModelLimits
	if client != nil {
		if info, ok := client.CatalogEntry(model); ok {
			l.ContextWindow = info.ContextWindow
			l.InputPrice = info.Pricing.InputPerMTok
			l.OutputPrice = info.Pricing.OutputPerMTok
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
// Provider that reports no usage.
func (e *Engine) ContextTokens() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastCtxTokens
}
