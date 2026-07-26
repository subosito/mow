package acp

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

type modelProv struct {
	model string
	list  []mow.ModelInfo
}

func (p *modelProv) Chat(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec, hooks mow.ChatHooks) (mow.Message, error) {
	return mow.Message{Role: "assistant", Content: "ok"}, nil
}

func (p *modelProv) ListModels(ctx context.Context) ([]mow.ModelInfo, error) {
	return append([]mow.ModelInfo(nil), p.list...), nil
}

func (p *modelProv) SetModel(id string) error {
	p.model = id
	return nil
}

func TestFilterChatModels(t *testing.T) {
	in := []mow.ModelInfo{
		// Plain OpenAI-compatible catalog (id only) → keep all.
		{ID: "deepseek-chat"},
		{ID: "gpt-5-mini"},
		// Optional gateway wire metadata.
		{ID: "chat-model", Wire: "openai-chat-completions"},
		{ID: "chat-model:image", Wire: "openai-chat-completions"},
		{ID: "tts-model", Wire: "openai-audio-speech"},
		{ID: "image-model", Wire: "openai-images-generations"},
		{ID: "claude-model", Wire: "anthropic-messages"},
	}
	got := filterChatModels(in)
	want := map[string]bool{
		"deepseek-chat": true,
		"gpt-5-mini":    true,
		"chat-model":    true,
		"claude-model":  true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if !want[m.ID] {
			t.Fatalf("unexpected %q", m.ID)
		}
	}
}

func TestModelConfigOptionShape(t *testing.T) {
	prov := &modelProv{
		model: "alpha",
		list: []mow.ModelInfo{
			{ID: "alpha", Wire: "openai-chat-completions"},
			{ID: "beta", Wire: "anthropic-messages"},
			{ID: "eleven_v3", Wire: "openai-audio-speech"},
			{ID: "alpha:image", Wire: "openai-chat-completions"},
		},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "alpha", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng}
	opt := a.modelConfigOption(context.Background())
	if opt == nil {
		t.Fatal("expected model option")
	}
	if opt["id"] != configIDModel || opt["type"] != "select" || opt["category"] != "model" {
		t.Fatalf("%v", opt)
	}
	if opt["currentValue"] != "alpha" {
		t.Fatalf("current=%v", opt["currentValue"])
	}
	opts, _ := opt["options"].([]map[string]any)
	if len(opts) != 2 {
		t.Fatalf("options=%v (want chat only)", opts)
	}
	// No wire in display name.
	for _, o := range opts {
		name, _ := o["name"].(string)
		if strings.Contains(name, "[") {
			t.Fatalf("wire leaked into name: %q", name)
		}
	}
	if err := a.applyModelConfig(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	if eng.Model() != "beta" || prov.model != "beta" {
		t.Fatalf("model eng=%q prov=%q", eng.Model(), prov.model)
	}
}

func TestSessionConfigOptionsIncludesModeAndModel(t *testing.T) {
	prov := &modelProv{
		model: "alpha",
		list:  []mow.ModelInfo{{ID: "alpha", Wire: "openai-chat-completions"}},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "alpha", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng}
	opts := a.sessionConfigOptions(context.Background(), ModeAsk)
	if len(opts) != 2 {
		t.Fatalf("opts=%v", opts)
	}
	if opts[0]["id"] != configIDMode || opts[0]["category"] != "mode" || opts[0]["currentValue"] != ModeAsk {
		t.Fatalf("mode opt=%v", opts[0])
	}
	if opts[1]["id"] != configIDModel || opts[1]["category"] != "model" {
		t.Fatalf("model opt=%v", opts[1])
	}
}

func TestModelConfigIncludesCurrentWhenMissingFromCatalog(t *testing.T) {
	prov := &modelProv{
		model: "custom-local",
		list:  []mow.ModelInfo{{ID: "catalog-a", Wire: "openai-chat-completions"}},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "custom-local", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng}
	opt := a.modelConfigOption(context.Background())
	if opt["currentValue"] != "custom-local" {
		t.Fatalf("%v", opt)
	}
	opts, _ := opt["options"].([]map[string]any)
	found := false
	for _, o := range opts {
		if o["value"] == "custom-local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("current missing from options: %v", opts)
	}
}
