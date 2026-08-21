package engine_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/subosito/mow"
)

// An explicit CLI model is installed before the initial catalog refresh. Once
// that refresh completes it must receive the model's catalog default_effort,
// just like a model selected later through SetModel.
func TestNewExplicitModelAppliesCatalogDefaultEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":             "gpt-5-mini",
				"facet":          "chat",
				"efforts":        []string{"low", "medium", "high"},
				"default_effort": "medium",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	eng, err := mow.New(mow.Options{
		NoSession:     true,
		BaseURL:       srv.URL + "/v1",
		Model:         "gpt-5-mini", // supplied by --model via cliutil.EngineFlags
		ExplicitModel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if got := eng.Effort(); got != "medium" {
		t.Fatalf("Effort()=%q want catalog default %q", got, "medium")
	}
}

func TestSetModelAdoptsCatalogDefaultEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "grok-4.6",
					"facet":          "chat",
					"efforts":        []string{"low", "high", "xhigh"},
					"default_effort": "high",
				},
				{
					"id":             "kimi-k3",
					"facet":          "chat",
					"efforts":        []string{"low", "high", "max"},
					"default_effort": "max",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	eng, err := mow.New(mow.Options{
		NoSession:     true,
		BaseURL:       srv.URL + "/v1",
		Model:         "grok-4.6",
		ExplicitModel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if got := eng.Effort(); got != "high" {
		t.Fatalf("initial Effort()=%q want grok default high", got)
	}
	if err := eng.SetModel("kimi-k3"); err != nil {
		t.Fatal(err)
	}
	if got := eng.Effort(); got != "max" {
		t.Fatalf("after SetModel(kimi-k3) Effort()=%q want catalog default max", got)
	}
}

// A provider-prefixed model id must still pick up catalog default_effort
// when GET /models publishes the bare id (cs/gemini-x vs gemini-x).
func TestNewPrefixedModelAdoptsBareCatalogDefaultEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":             "gemini-3.7-flash",
				"facet":          "chat",
				"efforts":        []string{"low", "medium", "high"},
				"default_effort": "high",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	eng, err := mow.New(mow.Options{
		NoSession:     true,
		BaseURL:       srv.URL + "/v1",
		Model:         "cs/gemini-3.7-flash",
		ExplicitModel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if got := eng.Effort(); got != "high" {
		t.Fatalf("Effort()=%q want catalog default high", got)
	}
	if got := eng.DisplayEffort(); got != "high" {
		t.Fatalf("DisplayEffort()=%q want high (must match adopted Client.Effort)", got)
	}
}

