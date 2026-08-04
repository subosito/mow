package llm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Effort levels (canonical). Empty = provider / gateway default.
// Effort is never encoded in the model id on the wire — gateways that need
// upstream tier suffixes map that themselves.
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

// NormalizeEffort canonicalizes an effort string against the static set
// none|low|medium|high. Empty / default / auto → "" (unset).
// Prefer NormalizeEffortFor when the model advertises a catalog efforts list.
func NormalizeEffort(s string) (string, error) {
	return NormalizeEffortFor(s, nil)
}

// NormalizeEffortFor canonicalizes effort. When allowed is non-empty, only those
// values (plus empty/default/auto → "") are accepted — no static list.
// When allowed is empty, falls back to none|low|medium|high (with none aliases).
func NormalizeEffortFor(s string, allowed []string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "default", "auto":
		return "", nil
	}
	if len(allowed) > 0 {
		for _, a := range allowed {
			if strings.EqualFold(strings.TrimSpace(a), s) {
				return strings.ToLower(strings.TrimSpace(a)), nil
			}
		}
		return "", fmt.Errorf("effort must be one of %s (or empty), got %q", strings.Join(allowed, "|"), s)
	}
	switch s {
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

// CollapseEffortTiersInCatalog returns a lean catalog for pickers: ids with
// legacy -low|medium|high suffixes collapse to the base name once.
// Wire / Efforts metadata is preserved from the first entry that has them.
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
			if len(existing.info.Efforts) == 0 && len(m.Efforts) > 0 {
				existing.info.Efforts = append([]string(nil), m.Efforts...)
			}
			if existing.info.DefaultEffort == "" && strings.TrimSpace(m.DefaultEffort) != "" {
				existing.info.DefaultEffort = strings.TrimSpace(m.DefaultEffort)
			}
			if existing.info.Facet == "" && strings.TrimSpace(m.Facet) != "" {
				existing.info.Facet = strings.TrimSpace(m.Facet)
			}
			if existing.info.ContextWindow == 0 && m.ContextWindow > 0 {
				existing.info.ContextWindow = m.ContextWindow
			}
			if existing.info.MaxOutputTokens == 0 && m.MaxOutputTokens > 0 {
				existing.info.MaxOutputTokens = m.MaxOutputTokens
			}
			if existing.info.Pricing.InputPerMTok == 0 && m.Pricing.InputPerMTok > 0 {
				existing.info.Pricing = m.Pricing
			}
			continue
		}
		order = append(order, dkey)
		info := m
		info.ID = displayID
		info.Wire = strings.TrimSpace(m.Wire)
		if len(m.Efforts) > 0 {
			info.Efforts = append([]string(nil), m.Efforts...)
		}
		info.DefaultEffort = strings.TrimSpace(m.DefaultEffort)
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
// Example: "gemini-2.5-flash-medium" + "" → ("gemini-2.5-flash", "medium").
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
// always the lean id (suffixes stripped). Gateways that need upstream tier
// strings map them server-side from catalog efforts + body reasoning_effort.
//
// When modelEfforts is non-empty (from GET /v1/models), effort is always sent as
// reasoning_effort — including Gemini/Cloud Code — so the gateway can rewrite
// the upstream model. Legacy thinking_budget injection only applies when the
// catalog does not advertise efforts and the model looks like Gemini.
func ResolveEffort(model, wire, effort string, modelEfforts []string) EffortPlan {
	plan := EffortPlan{Model: StripEffortTiers(model)}
	eff, err := NormalizeEffortFor(effort, modelEfforts)
	if err != nil || eff == "" {
		return plan
	}

	switch NormalizeWire(wire) {
	case WireOpenAIChat, WireOpenAIResponses:
		if len(modelEfforts) > 0 {
			// Catalog-driven: always body reasoning_effort; gateway maps tiers.
			if eff != EffortNone {
				plan.ReasoningEffort = eff
			}
		} else if looksGeminiFamily(plan.Model) {
			// Legacy path when gateway has no efforts metadata.
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
	// Public Gemini product ids and common vendor-prefixed forms (e.g.
	// vendor/gemini-…). Do not match private gateway catalog prefixes here.
	return strings.Contains(m, "gemini") ||
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

// activeNativeTools returns the provider-executed tool declarations to merge
// into the request.
//
// Local config wins when set, so an operator can disable a catalog tool or add
// one the gateway does not publish. Otherwise the catalog decides: capability
// is a property of the model, and the gateway already knows it, so clients do
// not each have to repeat the same list — nor can they claim a tool the model
// cannot run.
func (c *Client) activeNativeTools() []map[string]any {
	if c == nil {
		return nil
	}
	if len(c.NativeTools) > 0 {
		return c.NativeTools
	}
	if len(c.CatalogModels) == 0 {
		return nil
	}
	id := strings.TrimSpace(StripEffortTiers(c.Model))
	if info, ok := c.CatalogModels[strings.ToLower(id)]; ok && len(info.NativeTools) > 0 {
		return info.NativeTools
	}
	if info, ok := c.CatalogModels[id]; ok && len(info.NativeTools) > 0 {
		return info.NativeTools
	}
	return nil
}

// modelEfforts returns catalog-advertised efforts for the active model, if any.
func (c *Client) modelEfforts() []string {
	if c == nil || len(c.CatalogModels) == 0 {
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(StripEffortTiers(c.Model)))
	if info, ok := c.CatalogModels[id]; ok && len(info.Efforts) > 0 {
		return info.Efforts
	}
	// Case-sensitive fallback (ids are usually already lower).
	if info, ok := c.CatalogModels[strings.TrimSpace(StripEffortTiers(c.Model))]; ok && len(info.Efforts) > 0 {
		return info.Efforts
	}
	return nil
}

// requestModel returns the lean model id for the request body.
func (c *Client) requestModel() string {
	if c == nil {
		return ""
	}
	return ResolveEffort(c.Model, c.Wire, c.Effort, c.modelEfforts()).Model
}

// finalizeChatBody injects effort body fields and ensures lean model id.
func (c *Client) finalizeChatBody(raw []byte) ([]byte, error) {
	if c == nil || len(raw) == 0 {
		return raw, nil
	}
	plan := ResolveEffort(c.Model, c.Wire, c.Effort, c.modelEfforts())
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, err
	}
	cur, _ := m["model"].(string)
	needModel := plan.Model != "" && plan.Model != cur
	needBody := plan.ReasoningEffort != "" || plan.ThinkingBudget != nil
	native := c.activeNativeTools()
	needTools := len(native) > 0
	if !needModel && !needBody && !needTools {
		return raw, nil
	}
	if needTools {
		// chat-completions has no server-tool channel: the provider drops the
		// declaration without an error, so the model answers from training
		// data as if it had searched. Silent staleness is worse than a loud
		// failure — say so once rather than let it look like it worked.
		if NormalizeWire(c.Wire) == WireOpenAIChat {
			c.warnNativeToolsUnsupported()
		}
		m["tools"] = mergeNativeTools(m["tools"], native)
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

// SetCatalogModels stores lean ModelInfo entries (with efforts) from ListModels.
func (c *Client) SetCatalogModels(list []ModelInfo) {
	if c == nil {
		return
	}
	m := make(map[string]ModelInfo, len(list))
	for _, info := range list {
		id := strings.TrimSpace(info.ID)
		if id == "" {
			continue
		}
		// Index by lower-case for lookup; keep original id on the value.
		cp := info
		if len(info.Efforts) > 0 {
			cp.Efforts = append([]string(nil), info.Efforts...)
		}
		m[strings.ToLower(id)] = cp
	}
	c.CatalogModels = m
}

// CatalogEntry returns the cached GET /models entry for id (exact, then
// effort-tier stripped). Empty ID on miss. Filled by ListModels.
func (c *Client) CatalogEntry(model string) (ModelInfo, bool) {
	if c == nil || len(c.CatalogModels) == 0 {
		return ModelInfo{}, false
	}
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return ModelInfo{}, false
	}
	if info, ok := c.CatalogModels[key]; ok {
		return info, true
	}
	// Lean base after AG effort-tier collapse (gemini-x-medium → gemini-x).
	base := strings.ToLower(StripEffortTiers(key))
	if base != key {
		if info, ok := c.CatalogModels[base]; ok {
			return info, true
		}
	}
	return ModelInfo{}, false
}

// EffortsForModel returns catalog efforts for a model id, or nil.
func (c *Client) EffortsForModel(model string) []string {
	if c == nil || len(c.CatalogModels) == 0 {
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(StripEffortTiers(model)))
	if info, ok := c.CatalogModels[id]; ok {
		return append([]string(nil), info.Efforts...)
	}
	return nil
}

// DefaultEffortForModel returns catalog default_effort for a model id, or "".
func (c *Client) DefaultEffortForModel(model string) string {
	if c == nil || len(c.CatalogModels) == 0 {
		return ""
	}
	id := strings.ToLower(strings.TrimSpace(StripEffortTiers(model)))
	if info, ok := c.CatalogModels[id]; ok {
		return strings.TrimSpace(info.DefaultEffort)
	}
	return ""
}

// SyncEffortToModel ensures engine effort is allowed for model.
// If current effort is empty or not in catalog efforts, sets default_effort
// (or clears when default is empty). No-op when model has no efforts metadata.
func (c *Client) SyncEffortToModel(model string) {
	if c == nil {
		return
	}
	allowed := c.EffortsForModel(model)
	if len(allowed) == 0 {
		return
	}
	cur := strings.ToLower(strings.TrimSpace(c.Effort))
	if cur != "" {
		for _, a := range allowed {
			if strings.EqualFold(a, cur) {
				return
			}
		}
	}
	c.Effort = strings.ToLower(strings.TrimSpace(c.DefaultEffortForModel(model)))
}

// mergeNativeTools appends provider-executed tool declarations to whatever the
// wire already built, skipping ones already present by "type". Local function
// tools and native tools share one array on every wire, so this must add to
// the existing value rather than replace it — overwriting would silently drop
// the agent's own tools and strand the loop.
func mergeNativeTools(existing any, native []map[string]any) []any {
	var out []any
	seen := map[string]bool{}
	if cur, ok := existing.([]any); ok {
		out = append(out, cur...)
		for _, it := range cur {
			if mm, ok := it.(map[string]any); ok {
				if t, _ := mm["type"].(string); t != "" {
					seen[t] = true
				}
			}
		}
	}
	for _, nt := range native {
		if len(nt) == 0 {
			continue
		}
		if t, _ := nt["type"].(string); t != "" {
			if seen[t] {
				continue
			}
			seen[t] = true
		}
		out = append(out, nt)
	}
	return out
}

// nativeToolWarned dedupes the unsupported-wire warning by model. The guard
// lives here rather than on Client because Client is copied by value (the
// engine clones it per model switch), which rules out any embedded sync type.
var nativeToolWarned sync.Map // model -> struct{}

// warnNativeToolsUnsupported logs once per model that native tools were
// configured on a wire that cannot carry them, so a long agent loop does not
// repeat it on every call.
func (c *Client) warnNativeToolsUnsupported() {
	if _, dup := nativeToolWarned.LoadOrStore(c.Model, struct{}{}); dup {
		return
	}
	types := make([]string, 0, len(c.activeNativeTools()))
	for _, t := range c.activeNativeTools() {
		if s, _ := t["type"].(string); s != "" {
			types = append(types, s)
		}
	}
	slog.Warn("llm: native_tools ignored on this wire",
		"wire", WireOpenAIChat,
		"model", c.Model,
		"tools", strings.Join(types, ","),
		"detail", "chat-completions has no server-tool channel; the provider drops the declaration and the model answers from training data. Use openai-responses or anthropic-messages.")
}
