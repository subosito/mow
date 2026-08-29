package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// Prompt must not deadlock when scaling max_context_chars from Limits while
// holding e.mu (regression: Limits re-locked mu under PromptWith).
func TestPromptNoDeadlockOnLimits(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng, err := New(Options{
			NoSession: true,
			Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
				return Message{Role: "assistant", Content: "ok"}, nil
			},
		})
		if err != nil {
			t.Error(err)
			return
		}
		defer eng.Close()
		if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Prompt deadlocked (likely Limits under e.mu)")
	}
}

func TestResolveMaxContextChars(t *testing.T) {
	if got := resolveMaxContextChars(-1, 1_000_000); got != 0 {
		t.Fatalf("disabled → %d", got)
	}
	// Auto + known window: char budget stays 0 so applyCompact uses tokens.
	if got := resolveMaxContextChars(0, 1_000_000); got != 0 {
		t.Fatalf("auto 1M → %d want 0 (token path)", got)
	}
	if got := resolveMaxContextChars(0, 200_000); got != 0 {
		t.Fatalf("auto 200k → %d want 0 (token path)", got)
	}
	if got := resolveMaxContextChars(200_000, 1_000_000); got != 200_000 {
		t.Fatalf("explicit opt → %d", got)
	}
	if got := resolveMaxContextChars(0, 0); got != agent.DefaultMaxContextChars {
		t.Fatalf("auto, no window → %d", got)
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

func TestEstimatePromptCost_fromLastTokens(t *testing.T) {
	eng, err := New(Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// Seed lastProviderTokens as if a prior provider report landed.
	eng.mu.Lock()
	eng.lastProviderTokens = 50_000
	if eng.cfg != nil {
		eng.cfg.LLM.InputPrice = 3 // $3 / 1M
		eng.cfg.LLM.ContextWindow = 200_000
	}
	eng.mu.Unlock()

	est := eng.EstimatePromptCost()
	if !est.FromProvider || est.InputTokens != 50_000 {
		t.Fatalf("est=%+v want FromProvider 50000", est)
	}
	// 50k / 1e6 * 3 = 0.15
	if est.InputUSD < 0.149 || est.InputUSD > 0.151 {
		t.Fatalf("InputUSD=%v want ~0.15", est.InputUSD)
	}
	if est.ContextWindow != 200_000 {
		t.Fatalf("ContextWindow=%d", est.ContextWindow)
	}
}

func TestEstimatePromptCost_fromPriorChars(t *testing.T) {
	eng, err := New(Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// No lastProviderTokens — estimate from prior history bulk.
	eng.mu.Lock()
	eng.lastProviderTokens = 0
	// ~40k chars → ~10k tokens at 4 chars/token
	eng.prior = []llm.Message{{Role: "user", Content: strings.Repeat("x", 40_000)}}
	eng.mu.Unlock()

	est := eng.EstimatePromptCost()
	if est.FromProvider {
		t.Fatalf("expected char estimate, got provider: %+v", est)
	}
	if est.InputTokens < 9_000 || est.InputTokens > 11_000 {
		t.Fatalf("InputTokens=%d want ~10000", est.InputTokens)
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
