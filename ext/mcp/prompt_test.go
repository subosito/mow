package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"gopkg.in/yaml.v3"
)

func TestServerPromptConfig(t *testing.T) {
	for _, input := range []string{
		"mcpServers:\n  search:\n    url: https://mcp.example/mcp\n    prompt: |\n      Prefer semantic search.\n      Use grep for symbols.\n",
		"servers:\n  - name: search\n    url: https://mcp.example/mcp\n    prompt: |\n      Prefer semantic search.\n      Use grep for symbols.\n",
		`{"mcpServers":{"search":{"url":"https://mcp.example/mcp","prompt":"Prefer semantic search.\nUse grep for symbols.\n"}}}`,
	} {
		var c Config
		if err := yaml.Unmarshal([]byte(input), &c); err != nil {
			t.Fatal(err)
		}
		got := c.resolved()
		if len(got) != 1 || got[0].Prompt != "Prefer semantic search.\nUse grep for symbols.\n" {
			t.Fatalf("prompt lost: %+v", got)
		}
	}
}

func TestServerPromptLifecycle(t *testing.T) {
	setupMCPTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{"protocolVersion": "2025-03-26"}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []any{}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()
	register := func(prompt string) {
		t.Helper()
		if err := RegisterServers([]ServerConfig{{Name: "search", URL: srv.URL, Prompt: prompt, MinTurns: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	render := func(turn int) string {
		t.Helper()
		var parts []string
		for _, fn := range ext.UserPromptHooks() {
			d, err := fn(ext.WithTurn(context.Background(), turn), ext.UserPromptEvent{})
			if err != nil {
				t.Fatal(err)
			}
			if d.SystemAppend != "" {
				parts = append(parts, d.SystemAppend)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	register("  Find concepts.\nUse grep for names.  ")
	if got := render(1); got != "" {
		t.Fatalf("dormant guidance: %q", got)
	}
	want := "MCP server search guidance:\nFind concepts.\nUse grep for names."
	for range 3 {
		if got := render(2); got != want {
			t.Fatalf("active/repeated guidance: %q", got)
		}
	}

	// Check what the model actually receives, including repeated requests and
	// a later disable on an already-created Engine.
	expectedCount := 1
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(_ context.Context, messages []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
		count := 0
		for _, m := range messages {
			if m.Role == "system" {
				count += strings.Count(m.Content, want)
			} else if strings.Contains(m.Content, want) {
				t.Error("guidance leaked into conversation")
			}
		}
		if count != expectedCount {
			t.Errorf("model saw guidance %d times, want %d", count, expectedCount)
		}
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	for range 2 {
		if _, err := eng.Prompt(ext.WithTurn(context.Background(), 2), "hello"); err != nil {
			t.Fatal(err)
		}
	}
	ext.SetExtensionEnabled("mcp:search", false)
	expectedCount = 0
	if _, err := eng.Prompt(ext.WithTurn(context.Background(), 3), "hello again"); err != nil {
		t.Fatal(err)
	}
	ext.SetExtensionEnabled("mcp:search", false)
	if got := render(3); got != "" {
		t.Fatalf("disabled guidance: %q", got)
	}
	ext.SetExtensionEnabled("mcp:search", true)
	if got := render(0); got != want {
		t.Fatalf("manual enable: %q", got)
	}
	old := ext.UserPromptHooks()
	register("Replacement.")
	if got := render(2); got != "MCP server search guidance:\nReplacement." {
		t.Fatalf("replacement: %q", got)
	}
	d, err := old[0](ext.WithTurn(context.Background(), 2), ext.UserPromptEvent{})
	if err != nil || d.SystemAppend != want {
		t.Fatalf("captured engine hook changed: %+v %v", d, err)
	}
	register(" \n ")
	if got := render(2); got != "" {
		t.Fatalf("blank replacement: %q", got)
	}
	register("Remove me.")
	if err := registerAllContext(ext.SetupContext{Workspace: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := render(2); got != "" {
		t.Fatalf("removed server: %q", got)
	}
	err = RegisterServers([]ServerConfig{{Name: "search", URL: srv.URL, Prompt: "Must roll back."}, {Name: "broken", URL: "ftp://invalid", Prompt: "Never registered."}})
	if err == nil {
		t.Fatal("expected setup failure")
	}
	if got := render(2); got != "" {
		t.Fatalf("failed setup leaked guidance: %q", got)
	}
}
