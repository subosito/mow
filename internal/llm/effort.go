package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Effort levels (canonical). Empty = provider / gateway default.
// Effort is never encoded in the model id on the wire — gateways that need
// upstream tier suffixes (e.g. Antigravity) map that themselves.
const (
	EffortNone   = "none"
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
)

// EffortPlan is the resolved request shape for one chat call.
type EffortPlan struct {
	Model           string // always the lean model id (no -low|-medium|-high)
	ReasoningEffort string // body reasoning_effort when set
	ThinkingBudget  *int   // body thinking_budget when set
}

var reEffortTiers = regexp.MustCompile(`(?i)^(.+)-(low|medium|high)$`)

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

// StripEffortTiers removes a trailing -low|-medium|-high tier from a model id.
// Used only to migrate legacy configs / collapse catalogs — not for request rewriting.
func StripEffortTiers(model string) string {
	base, _, ok := ParseEffortTiers(model)
	if ok {
		return base
	}
	return strings.TrimSpace(model)
}

// ParseEffortTiers splits a legacy tiered model id into base + effort.
func ParseEffortTiers(model string) (base, effort string, ok bool) {
	model = strings.TrimSpace(model)
	m := reEffortTiers.FindStringSubmatch(model)
	if len(m) != 3 {
		return model, "", false
	}
	return m[1], strings.ToLower(m[2]), true
}

// HasEffortTiers reports whether the model id ends with a legacy effort suffix.
func HasEffortTiers(model string) bool {
	_, _, ok := ParseEffortTiers(model)
	return ok
}

// CollapseEffortTiersInCatalog returns a lean catalog for pickers: ids with
// legacy -low|medium|high suffixes collapse to the base name once.
// Wire metadata is preserved from the first entry.
func CollapseEffortTiersInCatalog(list []ModelInfo) []ModelInfo {
	if len(list) == 0 {
		return nil
	}
	tieredBase := map[string]bool{}
	for _, m := range list {
		if base, _, ok := ParseEffortTiers(m.ID); ok {
			tieredBase[strings.ToLower(base)] = true
		}
	}
	type acc struct{ info ModelInfo }
	order := make([]string, 0, len(list))
	byKey := map[string]*acc{}
	for _, m := range list {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		base := StripEffortTiers(id)
		key := strings.ToLower(base)
		displayID := id
		if tieredBase[key] {
			displayID = base
		}
		dkey := strings.ToLower(displayID)
		if existing, ok := byKey[dkey]; ok {
			if existing.info.Wire == "" && strings.TrimSpace(m.Wire) != "" {
				existing.info.Wire = strings.TrimSpace(m.Wire)
			}
			if len(existing.info.Wires) == 0 && len(m.Wires) > 0 {
				existing.info.Wires = append([]string(nil), m.Wires...)
			}
			continue
		}
		order = append(order, dkey)
		info := m
		info.ID = displayID
		info.Wire = strings.TrimSpace(m.Wire)
		byKey[dkey] = &acc{info: info}
	}
	out := make([]ModelInfo, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k].info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NormalizeConfiguredModel migrates legacy tiered model ids into lean base + effort.
// Example: "gemini-3.6-flash-medium" + "" → ("gemini-3.6-flash", "medium").
// Explicit effort wins over a suffix. Non-tiered models are unchanged.
func NormalizeConfiguredModel(model, effort string) (base, eff string) {
	base = strings.TrimSpace(model)
	eff, _ = NormalizeEffort(effort)
	if b, t, ok := ParseEffortTiers(base); ok {
		base = b
		if eff == "" {
			eff = t
		}
	}
	return base, eff
}

// ResolveEffort maps canonical effort onto body fields. The request model is
// always the lean id (suffixes stripped). Gateways that still need upstream
// tier strings map that server-side from catalog config + optional body hints.
func ResolveEffort(model, wire, effort string, catalog []string) EffortPlan {
	_ = catalog // reserved for future catalog-driven body maps
	plan := EffortPlan{Model: StripEffortTiers(model)}
	eff, err := NormalizeEffort(effort)
	if err != nil || eff == "" {
		return plan
	}

	switch NormalizeWire(wire) {
	case WireOpenAIChat, WireOpenAIResponses:
		if looksGeminiFamily(plan.Model) {
			// Cloud Code / Gemini-compatible path (e.g. chacha thinking_budget).
			plan.ThinkingBudget = thinkingBudgetFor(eff)
		} else if eff != EffortNone {
			plan.ReasoningEffort = eff
		}
	case WireAnthropicMsg:
		// No portable effort body field yet.
	}
	return plan
}

func looksGeminiFamily(model string) bool {
	m := strings.ToLower(model)
	// Bare product names (gemini-3.6-flash) and legacy ag/ prefix.
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

// requestModel returns the lean model id for the request body.
func (c *Client) requestModel() string {
	if c == nil {
		return ""
	}
	return ResolveEffort(c.Model, c.Wire, c.Effort, c.CatalogIDs).Model
}

// finalizeChatBody injects effort body fields and ensures lean model id.
func (c *Client) finalizeChatBody(raw []byte) ([]byte, error) {
	if c == nil || len(raw) == 0 {
		return raw, nil
	}
	plan := ResolveEffort(c.Model, c.Wire, c.Effort, c.CatalogIDs)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, err
	}
	cur, _ := m["model"].(string)
	needModel := plan.Model != "" && plan.Model != cur
	needBody := plan.ReasoningEffort != "" || plan.ThinkingBudget != nil
	if !needModel && !needBody {
		return raw, nil
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

// SetCatalogIDs stores model ids from ListModels (lean after collapse).
func (c *Client) SetCatalogIDs(ids []string) {
	if c == nil {
		return
	}
	c.CatalogIDs = append([]string(nil), ids...)
}
