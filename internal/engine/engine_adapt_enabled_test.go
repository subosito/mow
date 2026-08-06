package engine

import (
	"context"
	"encoding/json"
	"testing"
)

type conditionalTool struct {
	fakeTool
	enabled bool
}

func (t *conditionalTool) Enabled(*Engine) bool { return t.enabled }

func TestPerEngineConditionalTool(t *testing.T) {
	chat := func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Role: "assistant", Content: "done"}, nil
	}
	eng, err := New(Options{
		NoSession: true,
		Chat:      chat,
		Tools: []Tool{
			&conditionalTool{fakeTool: fakeTool{name: "disabled"}},
			&conditionalTool{fakeTool: fakeTool{name: "enabled"}, enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if toolPresent(eng.tools, "disabled") {
		t.Fatal("disabled conditional tool is present")
	}
	if !toolPresent(eng.tools, "enabled") {
		t.Fatal("enabled conditional tool is absent")
	}
}

var _ interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Exec(context.Context, json.RawMessage) (string, error)
} = (*conditionalTool)(nil)
