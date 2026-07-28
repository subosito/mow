package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Effort levels (canonical). Empty = provider default when the model has no
// tiered family; for AG-style families, empty effort on a bare base defaults
// to medium at request time.
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
	base, _, ok := ParseEffortTier(model)
	if ok {
		return base
	}
	return strings.TrimSpace(model)
}

// ParseEffortTier splits model into base + tier when id ends with -low|medium|high.
func ParseEffortTier(model string) (base, effort string, ok bool) {
	model = strings.TrimSpace(model)
	m := reEffortTier.FindStringSubmatch(model)
	if len(m) != 3 {
		return model, "", false
	}
	return m[1], strings.ToLower(m[2]), true
}

// HasEffortTier reports whether the model id already ends with a known tier.
func HasEffortTier(model string) bool {
	_, _, ok := ParseEffortTier(model)
	return ok
}

// CollapseEffortTiersInCatalog returns a lean catalog for pickers: tiered
// families (ag/*-low|medium|high or any base that appears with those suffixes)
// are shown once as the base id. Wire metadata is preserved from the first
// chat-capable entry. Non-tiered ids are unchanged.
//
// CatalogIDs for ResolveEffort should still be the raw gateway list (full ids).
func CollapseEffortTiersInCatalog(list []ModelInfo) []ModelInfo {
	if len(list) == 0 {
		return nil
	}
	// Bases that appear with at least one tiered id.
	tieredBase := map[string]bool{}
	for _, m := range list {
		if base, _, ok := ParseEffortTier(m.ID); ok {
			tieredBase[strings.ToLower(base)] = true
		}
	}
	type acc struct {
		info ModelInfo
	}
	order := make([]string, 0, len(list))
	byKey := map[string]*acc{}
	for _, m := range list {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		base := StripEffortTier(id)
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

// NormalizeConfiguredModel splits a tiered config model into lean base + effort.
// If model is "ag/foo-medium" and effort is empty, returns base "ag/foo" and effort "medium".
// Non-tiered models are returned unchanged.
func NormalizeConfiguredModel(model, effort string) (base, eff string) {
	base = strings.TrimSpace(model)
	eff, _ = NormalizeEffort(effort)
	if b, t, ok := ParseEffortTier(base); ok {
		base = b
		if eff == "" {
			eff = t
		}
	}
	return base, eff
}

// ResolveEffort maps a canonical effort onto model id and/or request body fields.
// catalog is optional ids from GET /models (full tiered ids preferred for AG pick).
//
// For tiered families (ag/*, or catalog shows -low|medium|high variants):
//   - effort set → pick base-effort (catalog-aware fallback)
//   - effort unset + model already tiered → keep as-is
//   - effort unset + bare base → default medium for the request
func ResolveEffort(model, wire, effort string, catalog []string) EffortPlan {
	plan := EffortPlan{Model: strings.TrimSpace(model)}
	eff, err := NormalizeEffort(effort)
	if err != nil {
		return plan
	}
	base := StripEffortTier(plan.Model)
	if base == "" {
		return plan
	}

	if useModelIDTier(plan.Model, base, catalog) {
		if eff == "" {
			if HasEffortTier(plan.Model) {
				return plan // explicit tiered id, no separate effort
			}
			// Bare lean base (picker/config) → medium request id.
			if id := pickTierModel(base, EffortMedium, catalog); id != "" {
				plan.Model = id
			} else if len(catalog) == 0 {
				plan.Model = base + "-" + EffortMedium
			}
			return plan
		}
		if id := pickTierModel(base, eff, catalog); id != "" {
			plan.Model = id
			return plan
		}
		// Catalog miss: optimistic rewrite.
		if eff == EffortNone {
			plan.Model = base
		} else {
			plan.Model = base + "-" + eff
		}
		return plan
	}

	if eff == "" {
		return plan
	}

	// Body adapters when model-id tier is not applicable.
	switch NormalizeWire(wire) {
	case WireOpenAIChat, WireOpenAIResponses:
		if looksGeminiFamily(base) {
			plan.ThinkingBudget = thinkingBudgetFor(eff)
		} else if eff != EffortNone {
			plan.ReasoningEffort = eff
		}
	case WireAnthropicMsg:
		// No portable effort field yet.
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
		return candidates[0]
	}
	index := map[string]string{}
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
// requestModel() is already used for the model field; this injects body extras
// and corrects model when resolution rewrote it (e.g. bare AG base → -medium).
func (c *Client) finalizeChatBody(raw []byte) ([]byte, error) {
	if c == nil || len(raw) == 0 {
		return raw, nil
	}
	plan := ResolveEffort(c.Model, c.Wire, c.Effort, c.CatalogIDs)
	needBody := plan.ReasoningEffort != "" || plan.ThinkingBudget != nil
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, err
	}
	cur, _ := m["model"].(string)
	needModel := plan.Model != "" && plan.Model != cur
	if !needBody && !needModel {
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

// SetCatalogIDs stores raw model ids for effort tier resolution (from ListModels).
func (c *Client) SetCatalogIDs(ids []string) {
	if c == nil {
		return
	}
	c.CatalogIDs = append([]string(nil), ids...)
}
