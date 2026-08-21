package media_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	_ "github.com/subosito/mow/packs/media"
)

// specProvider captures the tool specs the Engine offers the model.
type specProvider struct{ names []string }

func (p *specProvider) Chat(ctx context.Context, msgs []mow.Message, tools []mow.ToolSpec, hooks mow.ChatHooks) (mow.Message, error) {
	p.names = nil
	for _, t := range tools {
		p.names = append(p.names, t.Function.Name)
	}
	return mow.Message{Role: "assistant", Content: "ok"}, nil
}

// mow-full blank-imports packs/media. A configured model plus tools.enable
// must still surface the tool to the model, exactly as it did when
// MediaTools was compiled into internal/engine.
func TestEngineOffersConfiguredMediaTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg := filepath.Join(home, "config.yaml")
	body := "llm:\n  model: test-model\nextensions:\n  media:\n    generate:\n      image: img-model\n" +
		"tools:\n  enable: [read, generate_image]\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	prov := &specProvider{}
	eng, err := mow.New(mow.Options{
		Provider:       prov,
		NoSession:      true,
		LoadUserConfig: true,
		ConfigPaths:    []string{cfg},
		Workspace:      t.TempDir(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if strings.Contains(logBuf.String(), "not registered") {
		t.Fatalf("configured media tool should not warn: %s", logBuf.String())
	}
	defer eng.Close()

	if _, err := eng.Prompt(t.Context(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	for _, n := range prov.names {
		if n == "generate_image" {
			return
		}
	}
	t.Fatalf("generate_image not offered; got %v", prov.names)
}
