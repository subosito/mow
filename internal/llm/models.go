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
// Wire/Wires are optional metadata some gateways attach; plain providers omit them.
type ModelInfo struct {
	ID      string   `json:"id"`
	OwnedBy string   `json:"owned_by,omitempty"`
	Wire    string   `json:"wire,omitempty"`  // preferred chat wire
	Wires   []string `json:"wires,omitempty"` // all registered wires
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
		out = append(out, ModelInfo{
			ID:      id,
			OwnedBy: m.OwnedBy,
			Wire:    strings.TrimSpace(m.Wire),
			Wires:   m.Wires,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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
