package acp

import (
	"context"
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

func TestModelConfigOptionShape(t *testing.T) {
	prov := &modelProv{
		model: "alpha",
		list: []mow.ModelInfo{
			{ID: "alpha", Wire: "openai-chat-completions"},
			{ID: "beta", Wire: "anthropic-messages"},
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
		t.Fatalf("options=%v", opts)
	}
	if err := a.applyModelConfig(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	if eng.Model() != "beta" || prov.model != "beta" {
		t.Fatalf("model eng=%q prov=%q", eng.Model(), prov.model)
	}
	opt2 := a.modelConfigOption(context.Background())
	if opt2["currentValue"] != "beta" {
		t.Fatalf("current after set=%v", opt2["currentValue"])
	}
}

func TestModelConfigIncludesCurrentWhenMissingFromCatalog(t *testing.T) {
	prov := &modelProv{
		model: "custom-local",
		list:  []mow.ModelInfo{{ID: "catalog-a"}},
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
