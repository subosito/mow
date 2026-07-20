package mow

import (
	"context"
	"encoding/json"
	"testing"
)

func TestIsReadOnlyTool(t *testing.T) {
	extRO := map[string]bool{"mcp_srv_lookup": true}
	cases := []struct {
		name string
		want bool
	}{
		{"read", true},
		{"glob", true},
		{"grep", true},
		{"understand_image", true},
		{"write", false},
		{"edit", false},
		{"bash", false},
		{"generate_image", false},
		{"mcp_srv_lookup", true},   // declared readOnlyHint
		{"mcp_srv_execute", false}, // undeclared ext tool
		{"acp_delegate", false},
	}
	for _, c := range cases {
		if got := isReadOnlyTool(c.name, extRO); got != c.want {
			t.Errorf("isReadOnlyTool(%q)=%v want %v", c.name, got, c.want)
		}
	}
}

func TestIsPowerTool(t *testing.T) {
	for name, want := range map[string]bool{
		"write": true, "edit": true, "bash": true, "BASH": true,
		"read": false, "grep": false, "mcp_x_y": false,
	} {
		if got := IsPowerTool(name); got != want {
			t.Errorf("IsPowerTool(%q)=%v want %v", name, got, want)
		}
	}
}

func TestRunResultUsageThreading(t *testing.T) {
	calls := 0
	res, err := Run(t.Context(), "hi", Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			calls++
			if calls == 1 {
				// One tool round-trip so usage must accumulate across calls.
				return Message{Role: "assistant", ToolCalls: []ToolCall{{
					ID: "c1", Type: "function",
					Function: FunctionCall{Name: "glob", Arguments: `{"path":"."}`},
				}}, Usage: Usage{InputTokens: 10, OutputTokens: 3}}, nil
			}
			return Message{Role: "assistant", Content: "done",
				Usage: Usage{InputTokens: 20, OutputTokens: 7}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.InputTokens != 30 || res.Usage.OutputTokens != 10 {
		t.Fatalf("usage=%+v want {30 10}", res.Usage)
	}
}

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

type fakeTool struct {
	name     string
	readOnly bool
	got      *string
}

func (t *fakeTool) Name() string                { return t.name }
func (t *fakeTool) Description() string         { return "fake" }
func (t *fakeTool) Parameters() json.RawMessage { return nil }
func (t *fakeTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if t.got != nil {
		*t.got = t.name
	}
	return "fake-result:" + t.name, nil
}
func (t *fakeTool) ReadOnly() bool { return t.readOnly }

func TestPerEngineTools(t *testing.T) {
	var called string
	chat := func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
		// First call: invoke the custom tool; second: finish.
		for _, m := range messages {
			if m.Role == "tool" {
				return Message{Role: "assistant", Content: "done " + m.Content}, nil
			}
		}
		return Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c1", Type: "function",
			Function: FunctionCall{Name: "lookup", Arguments: `{}`},
		}}}, nil
	}
	res, err := Run(t.Context(), "go", Options{
		NoSession: true,
		Chat:      chat,
		Tools:     []Tool{&fakeTool{name: "lookup", readOnly: true, got: &called}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called != "lookup" {
		t.Fatalf("per-engine tool not executed (called=%q, res=%q)", called, res.Text)
	}

	// Engine isolation: a second engine without the tool must not see it.
	eng2, err := New(Options{NoSession: true, Chat: chat})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range eng2.tools {
		if tool.Name() == "lookup" {
			t.Fatal("per-engine tool leaked into another engine")
		}
	}

	// Builtin collision is rejected.
	if _, err := New(Options{NoSession: true, Chat: chat,
		Tools: []Tool{&fakeTool{name: "read"}}}); err == nil {
		t.Fatal("builtin-name collision should error")
	}
}
