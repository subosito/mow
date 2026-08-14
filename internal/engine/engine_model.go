package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow/internal/llm"
)

// Model returns the active chat model id.
func (e *Engine) Model() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil && e.client.Model != "" {
		return e.client.Model
	}
	if e.cfg != nil {
		return e.cfg.LLM.Model
	}
	return ""
}

// SetModel switches the chat model for subsequent Prompt calls.
// Tiered AG-style ids (…-low|medium|high) are stored as the lean base; when
// effort is unset, the tier becomes the engine effort. No-op-safe: returns an
// error when using a custom Options.Chat inject (no live client).
func (e *Engine) SetModel(id string) error {
	if e == nil {
		return fmt.Errorf("mow: nil engine")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("mow: empty model id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	curEffort := ""
	if e.client != nil {
		curEffort = e.client.Effort
	} else if e.cfg != nil {
		curEffort = e.cfg.LLM.Effort
	}
	base, eff := llm.NormalizeConfiguredModel(id, curEffort)
	id = base
	if e.client == nil {
		// Custom providers may opt in via the ModelSwitcher extension.
		if sw, ok := e.provider.(ModelSwitcher); ok {
			if err := sw.SetModel(id); err != nil {
				return err
			}
			if e.cfg != nil {
				e.cfg.LLM.Model = id
				if strings.TrimSpace(e.cfg.LLM.Effort) == "" && eff != "" {
					e.cfg.LLM.Effort = eff
				}
			}
			return nil
		}
		return fmt.Errorf("mow: model switch requires the built-in client or a Provider implementing ModelSwitcher")
	}
	e.client.Model = id
	if strings.TrimSpace(e.client.Effort) == "" && eff != "" {
		e.client.Effort = eff
		e.client.EffortPinned = true
	}
	// Align effort with catalog efforts for this model (default_effort when needed).
	e.client.SyncEffortToModel(id)
	if e.cfg != nil {
		e.cfg.LLM.Model = id
		e.cfg.LLM.Effort = e.client.Effort
	}
	// Prefer catalog wire for this model when the user did not pin wire
	// (same as /model catalog pick). Under e.mu — do not call SetWire.
	e.applyPreferredWireLocked()
	// Validate against the catalog when available: reject model groups
	// (facet != "chat") and unknown ids with a clear error instead of
	// letting the gateway 400 on the next call.
	if e.client != nil {
		if info, ok := e.client.CatalogEntry(id); ok {
			if facet := strings.ToLower(strings.TrimSpace(info.Facet)); facet != "" && facet != "chat" {
				return fmt.Errorf("mow: model %q is a %s (not callable; pick a chat member from GET /v1/models)", id, info.Facet)
			}
		}
	}
	return nil
}

// SetWire switches the client wire (openai-chat-completions | openai-responses | anthropic-messages).
// Marks wire as explicit so catalog auto-align will not override it later.
func (e *Engine) SetWire(wire string) error {
	if e == nil {
		return fmt.Errorf("mow: nil engine")
	}
	wire = llm.NormalizeWire(wire)
	if !llm.IsKnownChatWire(wire) {
		return fmt.Errorf("mow: unsupported wire %q (want openai-chat-completions, openai-responses, or anthropic-messages)", wire)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return fmt.Errorf("mow: wire switch requires live LLM client")
	}
	e.client.Wire = wire
	e.wireExplicit = true
	if e.cfg != nil {
		e.cfg.LLM.Wire = wire
		e.cfg.LLM.WireExplicit = true
	}
	return nil
}

// applyPreferredWireFromCatalog sets client wire from GET /models metadata for
// the active model when wire was not user-pinned. Safe at any lock state.
func (e *Engine) applyPreferredWireFromCatalog() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applyPreferredWireLocked()
}

// applyPreferredWireLocked requires e.mu held.
func (e *Engine) applyPreferredWireLocked() {
	if e.wireExplicit || e.client == nil {
		return
	}
	info, ok := e.client.CatalogEntry(e.client.Model)
	if !ok {
		return
	}
	w := llm.NormalizeWire(info.Wire)
	if !llm.IsKnownChatWire(w) {
		return
	}
	if e.client.Wire == w {
		return
	}
	e.client.Wire = w
	if e.cfg != nil {
		e.cfg.LLM.Wire = w
	}
}

// Wire returns the active client wire id.
func (e *Engine) Wire() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil && e.client.Wire != "" {
		return e.client.Wire
	}
	if e.cfg != nil {
		return e.cfg.LLM.Wire
	}
	return llm.WireOpenAIChat
}

// ModelInfo is a listed model from GET /v1/models (wire / effort / facet /
// context window / pricing when the gateway publishes them).
type ModelInfo struct {
	ID            string
	Wire          string
	Wires         []string
	Efforts       []string // catalog-advertised; empty = use static none|low|medium|high
	DefaultEffort string
	// Facet is a gateway capability token ("chat", "image", "embed", …) for
	// models that need a different wire or endpoint. Non-chat facets are
	// filtered out of the agent loop by IsChatModel.
	//
	// Provider-executed search is not a facet: it is a tool on a normal chat
	// model. A gateway may still publish a "<model>:search" id; treat it as
	// non-chat, because without the tool declared in the request the model
	// emits a search call that nothing executes.
	// Empty when the catalog omits it. Chat UIs use FilterChatModels (facet
	// chat or empty only — never by parsing ":" in the model id).
	Facet string
	// From gateway catalog (0 = not published).
	ContextWindow   int
	MaxOutputTokens int
	// USD per 1M tokens when the gateway publishes pricing (0 = unknown).
	InputPrice  float64
	OutputPrice float64
}

// FilterChatModels keeps catalog rows suitable for agent chat pickers
// (mow REPL /model, the Rust mowi TUI over RPC, ACP). Rules:
//   - facet set → keep only empty or "chat" (gateway capability; do not parse
//     ":" from the model id — some providers use colon in the id)
//   - no wire metadata → keep (plain OpenAI-style catalogs are chat models)
//   - preferred wire set → keep only known chat wires; drop images/speech/…
func FilterChatModels(list []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(list))
	for _, m := range list {
		if IsChatModel(m) {
			out = append(out, m)
		}
	}
	return out
}

// IsChatModel reports whether m is suitable for the agent chat loop.
func IsChatModel(m ModelInfo) bool {
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return false
	}
	// Facet is authoritative when the gateway advertises it.
	if f := strings.ToLower(strings.TrimSpace(m.Facet)); f != "" && f != "chat" {
		return false
	}
	w := strings.TrimSpace(m.Wire)
	if w == "" {
		// Plain catalogs (OpenAI, DeepSeek, local servers, …): id only.
		return true
	}
	return llm.IsKnownChatWire(w)
}

// ListModels returns available models from GET /models.
// With a custom Chat inject and no live client, returns the current model alone when known.
func (e *Engine) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if e == nil {
		return nil, fmt.Errorf("mow: nil engine")
	}
	e.mu.Lock()
	client := e.client
	current := ""
	var catalogClient llm.Client
	if client != nil {
		current = client.Model
		catalogClient = cloneLLMClient(client)
	} else if e.cfg != nil {
		current = e.cfg.LLM.Model
	}
	e.mu.Unlock()

	if client == nil {
		// Custom providers may opt in via the ModelLister extension.
		if ml, ok := e.provider.(ModelLister); ok {
			return ml.ListModels(ctx)
		}
		if current != "" {
			return []ModelInfo{{ID: current}}, nil
		}
		return []ModelInfo{}, nil
	}
	infos, err := catalogClient.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	// Publish the refreshed immutable catalog under the same lock used by
	// request snapshots and model setters. The network call stayed lock-free.
	e.mu.Lock()
	if e.client == client {
		e.client.SetCatalogModels(infos)
	}
	e.mu.Unlock()
	out := make([]ModelInfo, 0, len(infos))
	for _, m := range infos {
		item := ModelInfo{
			ID: m.ID, Wire: m.Wire, Wires: m.Wires, DefaultEffort: m.DefaultEffort, Facet: m.Facet,
			ContextWindow: m.ContextWindow, MaxOutputTokens: m.MaxOutputTokens,
			InputPrice: m.Pricing.InputPerMTok, OutputPrice: m.Pricing.OutputPerMTok,
		}
		if len(m.Efforts) > 0 {
			item.Efforts = append([]string(nil), m.Efforts...)
		}
		out = append(out, item)
	}
	return out, nil
}

// SetModelWithWire sets model and, when wire is a known chat wire, switches client wire too.
// Used when GET /models returns preferred wire metadata.
func (e *Engine) SetModelWithWire(id, wire string) error {
	if err := e.SetModel(id); err != nil {
		return err
	}
	wire = strings.TrimSpace(wire)
	if wire == "" {
		return nil
	}
	if !llm.IsKnownChatWire(wire) {
		// Catalog may advertise media-only wires; keep current chat wire.
		return nil
	}
	return e.SetWire(wire)
}

// Effort returns the canonical reasoning effort (none|low|medium|high), or "" if unset.
// This is the session/user setting hosts should show in chrome. A running Prompt
// may send a cheaper tier (see Event.Effort on loop.run.start) without changing
// this value.
func (e *Engine) Effort() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		return e.client.Effort
	}
	if e.cfg != nil {
		return e.cfg.LLM.Effort
	}
	return ""
}

// SetEffort sets reasoning intensity for subsequent chat calls.
// When GET /models advertised efforts for the active model, only those values
// are accepted (no static none|low|medium|high list). Empty clears to gateway default.
func (e *Engine) SetEffort(effort string) error {
	if e == nil {
		return fmt.Errorf("mow: nil engine")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var allowed []string
	if e.client != nil {
		allowed = e.client.EffortsForModel(e.client.Model)
	}
	norm, err := llm.NormalizeEffortFor(effort, allowed)
	if err != nil {
		return err
	}
	if e.client != nil {
		e.client.Effort = norm
		// An explicit choice must survive later model switches when allowed.
		e.client.EffortPinned = norm != ""
	}
	if e.cfg != nil {
		e.cfg.LLM.Effort = norm
	}
	if e.client == nil && e.cfg == nil {
		return fmt.Errorf("mow: effort switch requires a live engine")
	}
	return nil
}

// simplePromptMaxRunes is the length ceiling for automatic effort downshift.
// Longer prompts keep the configured high/max effort.
const simplePromptMaxRunes = 120

// complexPromptKeywords mark turns that should keep high/max reasoning even
// when short (design, debugging, security, …). Matched case-insensitively as
// substrings of the user message.
var complexPromptKeywords = []string{
	"architect", "architecture", "design", "debug", "root cause",
	"why does", "why is", "why are", "analyze", "analyse", "refactor",
	"implement", "migrate", "security", "race condition", "deadlock",
	"performance", "algorithm", "investigate", "trade-off", "tradeoff",
	"thorough", "carefully", "from scratch", "step by step", "deep dive",
	"review the", "audit", "prove", "formal", "concurrency",
}

// isHighCostEffort reports whether effort is a high-spend reasoning tier
// (high, max, and common catalog synonyms). Empty/low/medium are not.
func isHighCostEffort(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high", "max", "xhigh", "ultra", "highest":
		return true
	default:
		return false
	}
}

// SuggestEffortForPrompt returns a recommended effort for one user message.
// When current is a high-cost tier and the prompt looks short and mechanical,
// it returns "medium" so the harness can downshift. Empty means leave current.
//
// allowed is the catalog efforts list for the active model (nil = static set).
// If medium is not allowed, returns "".
func SuggestEffortForPrompt(text, current string, allowed []string) string {
	if !isHighCostEffort(current) {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) > simplePromptMaxRunes {
		return ""
	}
	lower := strings.ToLower(text)
	for _, kw := range complexPromptKeywords {
		if strings.Contains(lower, kw) {
			return ""
		}
	}
	// medium must be valid for this model (catalog or static).
	if _, err := llm.NormalizeEffortFor(llm.EffortMedium, allowed); err != nil {
		return ""
	}
	return llm.EffortMedium
}

// applyAutoEffort temporarily downshifts high/max effort for a simple prompt.
// The cheaper tier is stored in runEffort so the request clone can send it
// without mutating Engine.Effort() (header/status/session stay on the user's
// selected tier). Restores by clearing the override.
func (e *Engine) applyAutoEffort(text string) (restore func()) {
	noop := func() {}
	if e == nil {
		return noop
	}
	e.mu.Lock()
	cur := ""
	var allowed []string
	if e.client != nil {
		cur = e.client.Effort
		allowed = e.client.EffortsForModel(e.client.Model)
	} else if e.cfg != nil {
		cur = e.cfg.LLM.Effort
	}
	want := SuggestEffortForPrompt(text, cur, allowed)
	if want == "" || want == cur {
		e.mu.Unlock()
		return noop
	}
	e.runEffort = want
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		e.runEffort = ""
		e.mu.Unlock()
	}
}

// requestEffortLocked returns the effort that will be sent on the current
// request. Callers must hold e.mu.
func (e *Engine) requestEffortLocked() string {
	if e.runEffort != "" {
		return e.runEffort
	}
	if e.client != nil {
		return e.client.Effort
	}
	if e.cfg != nil {
		return e.cfg.LLM.Effort
	}
	return ""
}

// requestEffort is the in-flight request effort (after any auto-downshift),
// or the session setting when idle. Hosts displaying chrome should use Effort().
func (e *Engine) requestEffort() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requestEffortLocked()
}

// Efforts returns catalog-advertised effort levels for the active model, or nil
// when the gateway did not advertise them (caller may fall back to static list).
func (e *Engine) Efforts() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return nil
	}
	return e.client.EffortsForModel(e.client.Model)
}

// DefaultEffort returns catalog default_effort for the active model, or "".
func (e *Engine) DefaultEffort() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return ""
	}
	return e.client.DefaultEffortForModel(e.client.Model)
}
