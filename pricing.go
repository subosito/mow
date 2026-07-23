package mow

import "strings"

// ModelLimits describes the active model's context window and (approximate)
// pricing. Prices are USD per 1M tokens; zero means unknown. The built-in table
// is best-effort — providers change prices, so override per deployment via
// llm.context_window / llm.input_price / llm.output_price.
type ModelLimits struct {
	ContextWindow int     // max context tokens (0 = unknown)
	InputPrice    float64 // USD per 1M input tokens (0 = unknown)
	OutputPrice   float64 // USD per 1M output tokens (0 = unknown)
}

// Cost returns the approximate USD cost of this usage under l (0 if prices
// unknown). With prompt caching the true input cost is lower (cached reads are
// discounted); this is an upper-bound estimate on the full input price.
func (u Usage) Cost(l ModelLimits) float64 {
	return float64(u.InputTokens)/1e6*l.InputPrice + float64(u.OutputTokens)/1e6*l.OutputPrice
}

// modelCatalog maps a model-id substring to limits. Order matters: more
// specific ids (…-mini) precede their prefixes so Contains picks the right row.
var modelCatalog = []struct {
	match string
	lim   ModelLimits
}{
	{"claude-opus", ModelLimits{200_000, 15, 75}},
	{"opus", ModelLimits{200_000, 15, 75}},
	{"claude-sonnet", ModelLimits{200_000, 3, 15}},
	{"sonnet", ModelLimits{200_000, 3, 15}},
	{"claude-haiku", ModelLimits{200_000, 0.8, 4}},
	{"haiku", ModelLimits{200_000, 0.8, 4}},
	{"gpt-4.1-mini", ModelLimits{1_000_000, 0.4, 1.6}},
	{"gpt-4.1", ModelLimits{1_000_000, 2, 8}},
	{"gpt-4o-mini", ModelLimits{128_000, 0.15, 0.6}},
	{"gpt-4o", ModelLimits{128_000, 2.5, 10}},
	{"o3-mini", ModelLimits{200_000, 1.1, 4.4}},
	{"o3", ModelLimits{200_000, 2, 8}},
	{"o1", ModelLimits{200_000, 15, 60}},
	{"deepseek", ModelLimits{128_000, 0.27, 1.1}},
}

// lookupModelLimits returns best-effort limits for a model id (empty when no
// catalog row matches).
func lookupModelLimits(model string) ModelLimits {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ModelLimits{}
	}
	for _, e := range modelCatalog {
		if strings.Contains(m, e.match) {
			return e.lim
		}
	}
	return ModelLimits{}
}

// Limits returns the active model's context window and pricing: the built-in
// catalog by model id, overridden by any llm.context_window / llm.input_price /
// llm.output_price config. Zero fields mean unknown.
func (e *Engine) Limits() ModelLimits {
	if e == nil {
		return ModelLimits{}
	}
	e.mu.Lock()
	model := ""
	if e.client != nil && e.client.Model != "" {
		model = e.client.Model
	} else if e.cfg != nil {
		model = e.cfg.LLM.Model
	}
	var cw int
	var ip, op float64
	if e.cfg != nil {
		cw, ip, op = e.cfg.LLM.ContextWindow, e.cfg.LLM.InputPrice, e.cfg.LLM.OutputPrice
	}
	e.mu.Unlock()

	l := lookupModelLimits(model)
	if cw > 0 {
		l.ContextWindow = cw
	}
	if ip > 0 {
		l.InputPrice = ip
	}
	if op > 0 {
		l.OutputPrice = op
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
