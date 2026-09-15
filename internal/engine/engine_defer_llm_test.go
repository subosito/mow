package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNewDeferLLMSkipsAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("MOW_HOME", t.TempDir())

	eng, err := New(Options{DeferLLM: true, NoSession: true, LoadUserConfig: false})
	if err != nil {
		t.Fatalf("New(DeferLLM): %v", err)
	}
	defer eng.Close()

	_, err = eng.Prompt(t.Context(), "hi")
	if err == nil {
		t.Fatal("Prompt without a key must fail")
	}
	if !strings.Contains(err.Error(), "api key required") {
		t.Fatalf("Prompt error = %v; want api key required", err)
	}
}

func TestNewSkipsUnconfiguredMediaTools(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	cfg := home + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("tools:\n  enable: [read, glob, grep, generate_image]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Options{
		DeferLLM:       true,
		NoSession:      true,
		LoadUserConfig: true,
		ConfigPaths:    []string{cfg},
	})
	if err != nil {
		t.Fatalf("New with generate_image enabled but no media model: %v", err)
	}
	defer eng.Close()
	for _, tl := range eng.tools {
		if tl.Name() == "generate_image" {
			t.Fatal("generate_image registered without extensions.media.generate.image")
		}
	}
}

// Under DeferLLM the client is built lazily by whichever call comes first:
// Prompt and the handshake readers (Efforts/DefaultEffort/DisplayEffort) may
// race on the first turn of `mow acp`. Run under -race.
func TestEnsureLLMConcurrentCallers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id":             "gpt-5-mini",
					"facet":          "chat",
					"efforts":        []string{"low", "medium", "high"},
					"default_effort": "medium",
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfg, []byte("llm:\n  base_url: "+srv.URL+"\n  api_key: k\n  model: gpt-5-mini\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := New(Options{DeferLLM: true, NoSession: true, LoadUserConfig: false, ConfigPaths: []string{cfg}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	var wg sync.WaitGroup
	errc := make(chan error, 1)
	wg.Go(func() {
		if _, perr := eng.Prompt(t.Context(), "hi"); perr != nil {
			select {
			case errc <- perr:
			default:
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			_ = eng.Efforts()
			_ = eng.DefaultEffort()
			_ = eng.DisplayEffort()
			_ = eng.Model()
			_ = eng.Wire()
		})
	}
	wg.Wait()
	select {
	case perr := <-errc:
		t.Fatalf("Prompt: %v", perr)
	default:
	}
	if got := eng.Efforts(); len(got) != 3 {
		t.Fatalf("Efforts=%v want catalog list", got)
	}
}
