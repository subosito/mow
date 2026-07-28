package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Effort levels (canonical). Empty = provider default (no rewrite, no body fields).
const (
	EffortNone   = "none"
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
)

// EffortPlan is the resolved request shape for one chat call.
type EffortPlan struct {
	Model           string // model id to send (may include tier suffix)
	ReasoningEffort string // body reasoning_effort when set
	ThinkingBudget  *int   // body thinking_budget when set
}

var effortTier = []string{EffortLow, EffortMedium, EffortHigh}

var reEffortTier = regexp.MustCompile(`(?i)^(.+)-(low|medium|high)$`)

// NormalizeEffort canonicalizes an effort string.
// Empty / default / auto → "" (unset). Invalid values return an error.
func NormalizeEffort(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "default", "auto":
		return "", nil
	case "none", "off", "minimal", "min":
		return EffortNone, nil
	case EffortLow, EffortMedium, EffortHigh:
		return s, nil
	default:
		return "", fmt.Errorf("effort must be none|low|medium|high (or empty), got %q", s)
	}
}

// StripEffortTier removes a trailing -low|-medium|-high tier from a model id.
func StripEffortTier(model string) string {
	model = strings.TrimSpace(model)
	if m := reEffortTier.FindStringSubmatch(model); len(m) == 3 {
		return m[1]
	}
	return model
}

// HasEffortTier reports whether the model id already ends with a known tier.
func HasEffortTier(model string) bool {
	return reEffortTier.MatchString(strings.TrimSpace(model))
}

// ResolveEffort maps a canonical effort onto model id and/or request body fields.
// catalog is optional lower/mixed-case ids from GET /models (used for tier pick + fallback).
func ResolveEffort(model, wire, effort string, catalog []string) EffortPlan {
	plan := EffortPlan{Model: strings.TrimSpace(model)}
	eff, err := NormalizeEffort(effort)
	if err != nil || eff == "" {
		return plan
	}
	base := StripEffortTier(plan.Model)
	if base == "" {
		return plan
	}

	if useModelIDTier(plan.Model, base, catalog) {
		if id := pickTierModel(base, eff, catalog); id != "" {
			plan.Model = id
			return plan
		}
	}

	// Body adapters for known wires when model-id tier is not applicable.
	switch NormalizeWire(wire) {
	case WireOpenAIChat, WireOpenAIResponses:
		if looksGeminiFamily(base) {
			plan.ThinkingBudget = thinkingBudgetFor(eff)
		} else if eff != EffortNone {
			plan.ReasoningEffort = eff
		}
		// none + non-gemini: omit body fields (provider default)
	case WireAnthropicMsg:
		// No portable effort field in our Anthropic body yet.
	}
	return plan
}

func useModelIDTier(model, base string, catalog []string) bool {
	if HasEffortTier(model) {
		return true
	}
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, "ag/") {
		return true
	}
	// Catalog shows this family is tiered.
	for _, id := range catalog {
		id = strings.TrimSpace(id)
		if strings.EqualFold(StripEffortTier(id), base) && HasEffortTier(id) {
			return true
		}
	}
	return false
}

func pickTierModel(base, effort string, catalog []string) string {
	var candidates []string
	switch effort {
	case EffortNone:
		candidates = []string{base, base + "-" + EffortLow}
	case EffortLow:
		candidates = []string{base + "-" + EffortLow, base + "-" + EffortMedium, base + "-" + EffortHigh, base}
	case EffortMedium:
		candidates = []string{base + "-" + EffortMedium, base + "-" + EffortHigh, base + "-" + EffortLow, base}
	case EffortHigh:
		candidates = []string{base + "-" + EffortHigh, base + "-" + EffortMedium, base + "-" + EffortLow, base}
	default:
		return ""
	}
	if len(catalog) == 0 {
		// Optimistic rewrite (gateway may 404; user can unset effort).
		return candidates[0]
	}
	index := map[string]string{} // lower → original casing
	for _, id := range catalog {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		index[strings.ToLower(id)] = id
	}
	for _, c := range candidates {
		if orig, ok := index[strings.ToLower(c)]; ok {
			return orig
		}
	}
	return ""
}

func looksGeminiFamily(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "ag/") ||
		strings.Contains(m, "gemini") ||
		strings.HasPrefix(m, "models/gemini")
}

func thinkingBudgetFor(effort string) *int {
	var n int
	switch effort {
	case EffortNone:
		n = 0
	case EffortLow:
		n = 256
	case EffortMedium:
		n = 1024
	case EffortHigh:
		n = 8192
	default:
		return nil
	}
	return &n
}

// requestModel returns the model id to put on the wire after effort resolution.
func (c *Client) requestModel() string {
	if c == nil {
		return ""
	}
	return ResolveEffort(c.Model, c.Wire, c.Effort, c.CatalogIDs).Model
}

// finalizeChatBody patches model / effort body fields into a marshaled JSON body.
func (c *Client) finalizeChatBody(raw []byte) ([]byte, error) {
	if c == nil || len(raw) == 0 {
		return raw, nil
	}
	plan := ResolveEffort(c.Model, c.Wire, c.Effort, c.CatalogIDs)
	eff, _ := NormalizeEffort(c.Effort)
	if eff == "" {
		return raw, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, err
	}
	if plan.Model != "" {
		m["model"] = plan.Model
	}
	if plan.ReasoningEffort != "" {
		m["reasoning_effort"] = plan.ReasoningEffort
	}
	if plan.ThinkingBudget != nil {
		m["thinking_budget"] = *plan.ThinkingBudget
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw, err
	}
	return out, nil
}

// SetCatalogIDs stores model ids for effort tier resolution (from ListModels).
func (c *Client) SetCatalogIDs(ids []string) {
	if c == nil {
		return
	}
	c.CatalogIDs = append([]string(nil), ids...)
}
