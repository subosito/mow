package acp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestModeState(t *testing.T) {
	m := modeState(ModeAsk)
	if m["currentModeId"] != ModeAsk {
		t.Fatal(m)
	}
	modes, _ := m["availableModes"].([]map[string]any)
	if len(modes) != 2 {
		t.Fatal(modes)
	}
}

func TestFilterChatModels(t *testing.T) {
	in := []mow.ModelInfo{
		// Plain OpenAI-compatible catalog (id only) → keep all.
		{ID: "deepseek-chat"},
		{ID: "gpt-5-mini"},
		// Optional gateway wire + facet metadata.
		{ID: "chat-model", Wire: "openai-chat-completions", Facet: "chat"},
		{ID: "chat-model:image", Wire: "openai-chat-completions", Facet: "image"},
		{ID: "chat-model:search", Wire: "openai-chat-completions", Facet: "search"},
		// Colon in id is NOT a facet signal when facet is chat (or empty).
		{ID: "vendor:org/model-v1", Wire: "openai-chat-completions", Facet: "chat"},
		{ID: "tts-model", Wire: "openai-audio-speech", Facet: "chat"},
		{ID: "image-model", Wire: "openai-images-generations"},
		{ID: "claude-model", Wire: "anthropic-messages", Facet: "chat"},
	}
	got := mow.FilterChatModels(in)
	want := map[string]bool{
		"deepseek-chat":       true,
		"gpt-5-mini":          true,
		"chat-model":          true,
		"vendor:org/model-v1": true,
		"claude-model":        true,
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
			{ID: "alpha", Wire: "openai-chat-completions", Facet: "chat"},
			{ID: "beta", Wire: "anthropic-messages", Facet: "chat"},
			{ID: "tts-model", Wire: "openai-audio-speech", Facet: "chat"},
			{ID: "alpha:image", Wire: "openai-chat-completions", Facet: "image"},
			{ID: "alpha:search", Wire: "openai-chat-completions", Facet: "search"},
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

func TestSessionConfigOptionsIncludesModeModelEffort(t *testing.T) {
	// Provider without catalog efforts → static effort list still advertised.
	prov := &modelProv{
		model: "alpha",
		list:  []mow.ModelInfo{{ID: "alpha", Wire: "openai-chat-completions"}},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "alpha", Effort: "high", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	a := &agentServer{eng: eng}
	opts := a.sessionConfigOptions(context.Background(), ModeAsk)
	if len(opts) < 2 {
		t.Fatalf("opts=%v", opts)
	}
	if opts[0]["id"] != configIDMode || opts[0]["category"] != "mode" || opts[0]["currentValue"] != ModeAsk {
		t.Fatalf("mode opt=%v", opts[0])
	}
	if opts[1]["id"] != configIDApprovals || opts[1]["currentValue"] != ApprovalPrompt {
		t.Fatalf("approvals opt=%v", opts[1])
	}
	if opts[2]["id"] != configIDModel || opts[2]["category"] != "model" {
		t.Fatalf("model opt=%v", opts[2])
	}
	// Provider ListModels has no efforts → static effort selector.
	if len(opts) < 4 || opts[3]["id"] != configIDEffort {
		t.Fatalf("want static effort option, got %v", opts)
	}
}

func TestApplyApprovalsConfig(t *testing.T) {
	a := &agentServer{sessions: map[string]*acpSession{}}
	if err := a.applyApprovalsConfig("s1", "always"); err != nil {
		t.Fatal(err)
	}
	if a.sessions["s1"].approvals != ApprovalAlways {
		t.Fatalf("%v", a.sessions["s1"])
	}
	a.activeSID = "s1"
	if a.sessionApprovals() != ApprovalAlways {
		t.Fatalf("got %q", a.sessionApprovals())
	}
	if err := a.applyApprovalsConfig("s1", "nope"); err == nil {
		t.Fatal("want error")
	}
}

func TestEffortConfigOptionPaintsCatalogDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":             "gpt-5-mini",
				"facet":          "chat",
				"efforts":        []string{"low", "medium", "high"},
				"default_effort": "medium",
			}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	eng, err := mow.New(mow.Options{
		NoSession:     true,
		BaseURL:       srv.URL + "/v1",
		Model:         "gpt-5-mini",
		ExplicitModel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	_, _ = eng.ListModels(context.Background())
	a := &agentServer{eng: eng}
	opt := a.effortConfigOption()
	if opt == nil {
		t.Fatal("want effort selector")
	}
	if opt["currentValue"] != "medium" {
		t.Fatalf("currentValue=%v want catalog default medium, not the default pseudo-id", opt["currentValue"])
	}
	opts, _ := opt["options"].([]map[string]any)
	var sawMediumDefault, sawDefaultId bool
	for _, o := range opts {
		if o["value"] == "default" {
			sawDefaultId = true
		}
		if o["value"] == "medium" {
			name, _ := o["name"].(string)
			if strings.Contains(name, "default") {
				sawMediumDefault = true
			}
		}
	}
	if sawDefaultId {
		t.Fatalf("must not advertise value=default: %v", opts)
	}
	if !sawMediumDefault {
		t.Fatalf("catalog default tier should be labeled (default): %v", opts)
	}
}

func TestEffortConfigOptionStaticHasNoDefaultPseudo(t *testing.T) {
	opt := effortConfigOptionStatic("")
	if opt["currentValue"] == "default" {
		t.Fatalf("empty effort must not advertise currentValue=default: %v", opt)
	}
	opts, _ := opt["options"].([]map[string]any)
	for _, o := range opts {
		if o["value"] == "default" {
			t.Fatalf("effort options must not include a default pseudo-id: %v", opts)
		}
	}
}

func TestEffortConfigOptionFromCatalog(t *testing.T) {
	// Simulate client catalog with multi-effort model via fake ListModels on provider
	// is hard without live client; unit-test the pure helper path via eng with no efforts.
	opt := effortConfigOptionStatic("medium")
	if opt["currentValue"] != "medium" {
		t.Fatalf("%v", opt)
	}
	// single-effort catalogs hide the selector (engine returns one effort).
	// Covered by effortConfigOption when eng.Efforts()==1 → nil.
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
