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
