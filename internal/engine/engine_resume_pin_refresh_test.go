package engine_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// A resumed session effort must stay pinned across catalog refreshes.
// Before the fix, resume restored the tier unpinned, so the first ListModels
// (ACP session/new → config options) silently snapped it back to default_effort.
func TestResumeEffortSurvivesCatalogRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id":             "model-a",
					"facet":          "chat",
					"efforts":        []string{"low", "high"},
					"default_effort": "low",
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	eng1, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      ws,
		BaseURL:        srv.URL + "/v1",
		Model:          "model-a",
		Effort:         "high",
		ExplicitEffort: true,
		SessionID:      "sess-pin-refresh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng1.Prompt(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	eng1.Close()

	eng2, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      ws,
		BaseURL:        srv.URL + "/v1",
		Model:          "model-a",
		SessionID:      "sess-pin-refresh",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng2.Close() })
	if got := eng2.Effort(); got != "high" {
		t.Fatalf("Effort()=%q want resumed high", got)
	}
	if _, err := eng2.ListModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := eng2.Effort(); got != "high" {
		t.Fatalf("Effort()=%q after catalog refresh, want high (resumed tier must not snap to default)", got)
	}
}
