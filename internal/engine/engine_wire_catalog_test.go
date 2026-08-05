package engine_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow"
)

// Catalog preferred wire is applied at New when the user did not pin llm.wire
// (e.g. --model claude-sonnet-4 with default openai-chat-completions).
func TestNewAppliesCatalogWireForModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":    "claude-sonnet-4",
					"wire":  "anthropic-messages",
					"facet": "chat",
				},
				{
					"id":    "gpt-x",
					"wire":  "openai-chat-completions",
					"facet": "chat",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	// Isolate from developer ~/.mow and env wire.
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_WIRE", "")
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	eng, err := mow.New(mow.Options{
		NoSession: true,
		BaseURL:   srv.URL + "/v1",
		Model:     "claude-sonnet-4",
		// Wire left default (openai-chat-completions) — not explicit.
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if got := eng.Wire(); got != "anthropic-messages" {
		t.Fatalf("wire=%q want anthropic-messages (catalog preferred for claude-sonnet-4)", got)
	}
	if eng.Model() != "claude-sonnet-4" {
		t.Fatalf("model=%q", eng.Model())
	}
}

// Explicit llm.wire must not be overridden by catalog preference.
func TestNewKeepsExplicitWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-sonnet-4", "wire": "anthropic-messages", "facet": "chat"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	// Pin OpenAI wire even for Claude (gateway O2A path).
	cfgPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("llm:\n  wire: openai-chat-completions\n  model: claude-sonnet-4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := mow.New(mow.Options{
		NoSession:   true,
		ConfigPaths: []string{cfgPath},
		BaseURL:     srv.URL + "/v1",
		Model:       "claude-sonnet-4",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if got := eng.Wire(); got != "openai-chat-completions" {
		t.Fatalf("explicit wire overridden: got %q", got)
	}
}

func TestSetModelAppliesCatalogWireWhenNotPinned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-sonnet-4", "wire": "anthropic-messages", "facet": "chat"},
				{"id": "gpt-x", "wire": "openai-chat-completions", "facet": "chat"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_WIRE", "")
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")

	eng, err := mow.New(mow.Options{
		NoSession: true,
		BaseURL:   srv.URL + "/v1",
		Model:     "gpt-x",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if eng.Wire() != "openai-chat-completions" {
		t.Fatalf("start wire=%q", eng.Wire())
	}
	if err := eng.SetModel("claude-sonnet-4"); err != nil {
		t.Fatal(err)
	}
	if got := eng.Wire(); got != "anthropic-messages" {
		t.Fatalf("after SetModel wire=%q want anthropic-messages", got)
	}
}
