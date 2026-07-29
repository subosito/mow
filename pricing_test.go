package mow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/subosito/mow/internal/agent"
)

func TestResolveMaxContextChars(t *testing.T) {
	if got := resolveMaxContextChars(0, 1_000_000); got != 0 {
		t.Fatalf("disabled → %d", got)
	}
	got := resolveMaxContextChars(agent.DefaultMaxContextChars, 1_000_000)
	if got <= agent.DefaultMaxContextChars {
		t.Fatalf("1M window should raise budget, got %d", got)
	}
	if got := resolveMaxContextChars(200_000, 1_000_000); got != 200_000 {
		t.Fatalf("explicit cfg → %d", got)
	}
	if got := resolveMaxContextChars(agent.DefaultMaxContextChars, 0); got != agent.DefaultMaxContextChars {
		t.Fatalf("no window → %d", got)
	}
}

func TestUsageCost(t *testing.T) {
	u := Usage{InputTokens: 1_000_000, OutputTokens: 500_000}
	l := ModelLimits{InputPrice: 3, OutputPrice: 15}
	// 1M*$3 + 0.5M*$15 = 3 + 7.5 = 10.5
	if got := u.Cost(l); got != 10.5 {
		t.Fatalf("cost=%v want 10.5", got)
	}
	if got := u.Cost(ModelLimits{}); got != 0 {
		t.Fatalf("unknown price cost=%v want 0", got)
	}
}

// Limits must come from GET /v1/models (gateway), not a client-side catalog.
func TestLimitsFromGatewayModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "gpt-5-mini",
					"context_window": 1_100_000,
					"pricing": map[string]any{
						"currency":        "USD",
						"input_per_mtok":  2.5,
						"output_per_mtok": 15.0,
					},
				},
			},
		})
	}))
	defer srv.Close()

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "gpt-5-mini")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")

	eng, err := New(Options{
		NoSession: true,
		BaseURL:   srv.URL + "/v1",
		Model:     "gpt-5-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// Prefer explicit ListModels (also what /model does) over racing the prefetch.
	if _, err := eng.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Prefetch may still be in flight; brief wait if needed.
	var lim ModelLimits
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lim = eng.Limits()
		if lim.ContextWindow == 1_100_000 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lim.ContextWindow != 1_100_000 {
		t.Fatalf("context_window=%d want 1100000 lim=%+v", lim.ContextWindow, lim)
	}
	if lim.InputPrice != 2.5 || lim.OutputPrice != 15 {
		t.Fatalf("prices=%v/%v want 2.5/15", lim.InputPrice, lim.OutputPrice)
	}
	// No speculative fallback for unknown models.
	eng.mu.Lock()
	if eng.client != nil {
		eng.client.Model = "totally-unknown-model-xyz"
	}
	eng.mu.Unlock()
	if got := eng.Limits(); got.ContextWindow != 0 || got.InputPrice != 0 {
		t.Fatalf("unknown model should not invent limits: %+v", got)
	}
}
