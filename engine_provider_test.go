package mow

import (
	"context"
	"testing"
)

type fakeProvider struct {
	model string
}

func (p *fakeProvider) Chat(ctx context.Context, messages []Message, tools []ToolSpec, hooks ChatHooks) (Message, error) {
	if hooks.OnToken != nil {
		hooks.OnToken("hel")
		hooks.OnToken("lo")
	}
	return Message{Role: "assistant", Content: "hello",
		Usage: Usage{InputTokens: 3, OutputTokens: 2}}, nil
}

func (p *fakeProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{{ID: "fake-1"}, {ID: "fake-2"}}, nil
}

func (p *fakeProvider) SetModel(id string) error {
	p.model = id
	return nil
}

func TestProviderSeam(t *testing.T) {
	prov := &fakeProvider{}
	var streamed string
	eng, err := New(Options{
		NoSession: true,
		Provider:  prov,
		OnToken:   func(d string) { streamed += d },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Prompt(t.Context(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hello" {
		t.Fatalf("text=%q", res.Text)
	}
	// Streaming works through the provider hooks (the old Chat seam could not).
	if streamed != "hello" {
		t.Fatalf("streamed=%q want hello", streamed)
	}
	if res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 2 {
		t.Fatalf("usage=%+v", res.Usage)
	}
	// Optional extensions keep the model surface functional.
	models, err := eng.ListModels(t.Context())
	if err != nil || len(models) != 2 {
		t.Fatalf("models=%v err=%v", models, err)
	}
	if err := eng.SetModel("fake-2"); err != nil {
		t.Fatal(err)
	}
	if prov.model != "fake-2" || eng.Model() != "fake-2" {
		t.Fatalf("switch: prov=%q eng=%q", prov.model, eng.Model())
	}
}
