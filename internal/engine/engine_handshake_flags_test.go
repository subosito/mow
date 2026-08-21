package engine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow"
)

// Handshake getters (effort.list / model.list / context) must work before
// Prompt on a DeferLLM engine — that is mow rpc. Combinations of --continue,
// --model, and --effort must not hide the chip or restore the wrong runtime.
func TestHandshakeFlagMatrix(t *testing.T) {
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
					"context_window": 200000,
				},
				{
					"id":             "kimi-k3",
					"facet":          "chat",
					"efforts":        []string{"low", "medium", "max"},
					"default_effort": "max",
					"context_window": 128000,
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	newBase := func() mow.Options {
		return mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			DeferLLM:       true,
			NoSession:      true,
		}
	}

	t.Run("fresh --model grok-4.6 paints catalog default", func(t *testing.T) {
		opt := newBase()
		opt.Model = "grok-4.6"
		opt.ExplicitModel = true
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.DisplayEffort(); got != "high" {
			t.Fatalf("DisplayEffort()=%q want high", got)
		}
		if n := len(eng.Efforts()); n == 0 {
			t.Fatal("Efforts() empty")
		}
		if eng.Limits().ContextWindow == 0 {
			t.Fatal("Limits() still zero; context chip would be blank")
		}
	})

	t.Run("fresh --model --effort low pins", func(t *testing.T) {
		opt := newBase()
		opt.Model = "grok-4.6"
		opt.ExplicitModel = true
		opt.Effort = "low"
		opt.ExplicitEffort = true
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.DisplayEffort(); got != "low" {
			t.Fatalf("DisplayEffort()=%q want low", got)
		}
	})

	t.Run("/model then /effort before Prompt", func(t *testing.T) {
		opt := newBase()
		opt.Model = "grok-4.6"
		opt.ExplicitModel = true
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if err := eng.SetModel("kimi-k3"); err != nil {
			t.Fatal(err)
		}
		if got := eng.DisplayEffort(); got != "max" {
			t.Fatalf("after SetModel DisplayEffort()=%q want max", got)
		}
		if err := eng.SetEffort("low"); err != nil {
			t.Fatal(err)
		}
		if got := eng.DisplayEffort(); got != "low" {
			t.Fatalf("after SetEffort DisplayEffort()=%q want low", got)
		}
	})

	sid := "sess-handshake"
	{
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			Model:          "kimi-k3",
			ExplicitModel:  true,
			Effort:         "medium",
			ExplicitEffort: true,
			SessionID:      sid,
			Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
				return mow.Message{Role: "assistant", Content: "ok"}, nil
			},
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eng.Prompt(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		eng.Close()
		found := false
		_ = filepath.WalkDir(home, func(p string, d os.DirEntry, _ error) error {
			if d != nil && !d.IsDir() && filepath.Base(p) == sid+".jsonl" {
				found = true
			}
			return nil
		})
		if !found {
			t.Fatal("session file not written")
		}
	}

	// A session whose last effort is a catalog-only tier ("xhigh" is not in
	// the static none|low|medium|high list). Persisted through the real
	// client path: SetEffort records the runtime event even with no Prompt.
	sidTier := "sess-handshake-tier"
	{
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			Model:          "grok-4.6",
			ExplicitModel:  true,
			DeferLLM:       true,
			SessionID:      sidTier,
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.SetEffort("xhigh"); err != nil {
			t.Fatal(err)
		}
		eng.Close()
	}

	t.Run("--continue restores catalog-only session effort", func(t *testing.T) {
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			DeferLLM:       true,
			Continue:       true,
			SessionID:      sidTier,
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.Model(); got != "grok-4.6" {
			t.Fatalf("Model()=%q want grok-4.6", got)
		}
		if got := eng.DisplayEffort(); got != "xhigh" {
			t.Fatalf("DisplayEffort()=%q want restored xhigh (catalog tier the static list would reject)", got)
		}
	})

	t.Run("--continue --effort low beats session tier", func(t *testing.T) {
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			DeferLLM:       true,
			Continue:       true,
			SessionID:      sidTier,
			Effort:         "low",
			ExplicitEffort: true,
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.DisplayEffort(); got != "low" {
			t.Fatalf("DisplayEffort()=%q want explicit low", got)
		}
	})

	t.Run("--continue restores session model+effort", func(t *testing.T) {
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			DeferLLM:       true,
			Continue:       true,
			SessionID:      sid,
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.Model(); got != "kimi-k3" {
			t.Fatalf("Model()=%q want kimi-k3", got)
		}
		if got := eng.DisplayEffort(); got != "medium" {
			t.Fatalf("DisplayEffort()=%q want restored medium", got)
		}
	})

	t.Run("--continue --model grok-4.6 uses catalog default not session effort", func(t *testing.T) {
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			DeferLLM:       true,
			Continue:       true,
			SessionID:      sid,
			Model:          "grok-4.6",
			ExplicitModel:  true,
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.Model(); got != "grok-4.6" {
			t.Fatalf("Model()=%q want grok-4.6", got)
		}
		if got := eng.DisplayEffort(); got != "high" {
			t.Fatalf("DisplayEffort()=%q want grok catalog high, not session medium", got)
		}
	})

	t.Run("--continue --model --effort low pins", func(t *testing.T) {
		opt := mow.Options{
			LoadUserConfig: true,
			Workspace:      ws,
			BaseURL:        srv.URL + "/v1",
			DeferLLM:       true,
			Continue:       true,
			SessionID:      sid,
			Model:          "grok-4.6",
			ExplicitModel:  true,
			Effort:         "low",
			ExplicitEffort: true,
		}
		eng, err := mow.New(opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		if got := eng.DisplayEffort(); got != "low" {
			t.Fatalf("DisplayEffort()=%q want explicit low", got)
		}
	})
}
