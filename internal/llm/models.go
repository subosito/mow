package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ModelInfo is one entry from GET /v1/models.
// Wire/Wires/Efforts/Facet are optional metadata some gateways attach; plain providers omit them.
// ContextWindow / Pricing come from gateway catalogs — not client speculation.
type ModelInfo struct {
	ID      string   `json:"id"`
	OwnedBy string   `json:"owned_by,omitempty"`
	Wire    string   `json:"wire,omitempty"`  // preferred chat wire
	Wires   []string `json:"wires,omitempty"` // all registered wires
	// Efforts lists allowed reasoning-effort values when the gateway advertises them.
	// When non-empty, clients should validate SetEffort against this list instead of
	// a static none|low|medium|high set.
	Efforts []string `json:"efforts,omitempty"`
	// DefaultEffort is the gateway default when effort is omitted.
	DefaultEffort string `json:"default_effort,omitempty"`
	// Facet is a gateway capability token ("chat", "image", "embed",
	// "speech_gen", …) for models that need a different wire or endpoint.
	//
	// Provider-executed search is NOT one of these: it is a tool on a normal
	// chat model, enabled by declaring it in the request (e.g. Responses
	// tools: [{"type":"web_search"}]). A gateway that publishes a separate
	// "<model>:search" id adds nothing — the bare id with the tool declared
	// behaves the same, and the split id without it makes the model emit a
	// tool call nothing executes. Prefer the bare model plus a declared tool.
	//
	// Empty on plain OpenAI catalogs. Do not infer from ":" in the model id.
	Facet string `json:"facet,omitempty"`
	// ContextWindow is max context tokens when the gateway publishes it (0 = unknown).
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the gateway's generation cap when published (0 = unknown).
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Pricing is gateway-published token pricing (USD per 1M tokens when set).
	Pricing ModelPricing `json:"pricing,omitempty"`
	// NativeTools are provider-executed tools this model can run, as wire-shaped
	// declarations the client merges into the request (e.g. [{"type":"web_search"}]).
	//
	// Capability belongs to the model, so publishing it here lets one gateway
	// answer for every client instead of each config repeating the same list —
	// and a client cannot declare a tool the model does not have. Empty on plain
	// provider catalogs; local llm.native_tools still overrides.
	NativeTools []map[string]any `json:"native_tools,omitempty"`
}

// ModelPricing is the optional pricing object on GET /v1/models entries.
// Chat metering uses InputPerMTok / OutputPerMTok (USD per 1M tokens).
// Media models may use PerUnit/Unit instead; those are ignored for chat Limits.
type ModelPricing struct {
	Currency          string  `json:"currency,omitempty"`
	InputPerMTok      float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok     float64 `json:"output_per_mtok,omitempty"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok,omitempty"`
	PerUnit           float64 `json:"per_unit,omitempty"`
	Unit              string  `json:"unit,omitempty"`
}

type modelsResponse struct {
	Data  []ModelInfo `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ListModels fetches available model ids from GET {base}/models (OpenAI-shaped).
// Optional wire metadata is accepted when present. Auth uses Bearer always;
// anthropic-messages also sends x-api-key + anthropic-version for native Anthropic hosts.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if c == nil {
		return nil, fmt.Errorf("llm: nil client")
	}
	if c.APIKey == "" {
		return nil, fmt.Errorf("llm: api key required")
	}
	url := modelsURL(c.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if NormalizeWire(c.Wire) == WireAnthropicMsg {
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("llm: models decode: %w (status %d body %s)", err, res.StatusCode, truncate(string(body), 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("llm: %s", parsed.Error.Message)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: models HTTP %d: %s", res.StatusCode, truncate(string(body), 300))
	}
	out := make([]ModelInfo, 0, len(parsed.Data))
	seen := map[string]bool{}
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		info := ModelInfo{
			ID:              id,
			OwnedBy:         m.OwnedBy,
			Wire:            strings.TrimSpace(m.Wire),
			Wires:           m.Wires,
			DefaultEffort:   strings.TrimSpace(m.DefaultEffort),
			Facet:           strings.TrimSpace(m.Facet),
			ContextWindow:   m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens,
			Pricing:         m.Pricing,
		}
		if len(m.Efforts) > 0 {
			info.Efforts = append([]string(nil), m.Efforts...)
		}
		if len(m.NativeTools) > 0 {
			info.NativeTools = append([]map[string]any(nil), m.NativeTools...)
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	collapsed := CollapseEffortTiersInCatalog(out)
	c.SetCatalogModels(collapsed)
	return collapsed, nil
}

func modelsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if strings.HasSuffix(base, "/chat/completions") {
		base = strings.TrimSuffix(base, "/chat/completions")
	}
	if strings.HasSuffix(base, "/messages") {
		base = strings.TrimSuffix(base, "/messages")
	}
	return base + "/models"
}
