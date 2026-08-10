package engine

import (
	"github.com/subosito/mow/internal/agent"
)

// ErrBudget ends a run that hit its spend ceiling. Re-exported from the agent
// loop so integrators errors.Is against one package.
var ErrBudget = agent.ErrBudget

// Spend ceiling wiring.
//
// The loop owns the gate (agent.PreModelFunc); the engine owns the policy —
// it is the layer that knows the configured limits and the model's published
// prices. Keeping the two apart means a host embedding the loop directly can
// install its own gate without inheriting mow's config shape.

// maxOutputTokens reports the reply cap that will actually apply, or 0 when
// nothing bounds it. A budget gate uses it to bound the call it is about to
// authorize instead of guessing.
//
// Mirrors the wire's own resolution order — explicit config, then the model's
// published cap — so the projection matches what the request will really carry.
func (e *Engine) maxOutputTokens() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return 0
	}
	if e.client.MaxTokens > 0 {
		return e.client.MaxTokens
	}
	if info, ok := e.client.CatalogEntry(e.client.Model); ok && info.MaxOutputTokens > 0 {
		return info.MaxOutputTokens
	}
	return 0
}

// budgetLimits reads the configured ceiling and pairs it with catalog pricing.
func (e *Engine) budgetLimits() agent.BudgetLimits {
	if e == nil {
		return agent.BudgetLimits{}
	}
	e.mu.Lock()
	maxTok, maxUSD := 0, 0.0
	if e.cfg != nil {
		maxTok, maxUSD = e.cfg.Policy.MaxRunTokens, e.cfg.Policy.MaxRunUSD
	}
	// Options override config: a host that sets an explicit ceiling for one
	// Engine must not be silently overruled by the user's yaml.
	if e.opt.MaxRunTokens > 0 {
		maxTok = e.opt.MaxRunTokens
	}
	if e.opt.MaxRunUSD > 0 {
		maxUSD = e.opt.MaxRunUSD
	}
	lim := e.limitsLocked()
	e.mu.Unlock()
	return agent.BudgetLimits{
		MaxTokens: maxTok,
		MaxUSD:    maxUSD,
		Prices: agent.TokenPrices{
			InputPerMTok:  lim.InputPrice,
			OutputPerMTok: lim.OutputPrice,
		},
	}
}

// budgetGate builds the PreModel spend gate, or nil when no limit is set.
//
// The error is deliberately propagated rather than swallowed: it means USD was
// requested for a model with no published price, and running anyway would give
// the operator a ceiling that can never fire.
func (e *Engine) budgetGate() (agent.PreModelFunc, error) {
	return agent.NewBudgetGate(e.budgetLimits())
}
