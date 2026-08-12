package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestPromptAllowedToolsLimitsSpecsAndExec(t *testing.T) {
	var specsSeen []string
	var extCalled string
	turn := 0
	eng, err := New(Options{
		NoSession:  true,
		AllowWrite: true,
		Tools:      []Tool{&fakeTool{name: "mcp_lookup", readOnly: true, got: &extCalled}},
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			turn++
			if turn == 1 {
				specsSeen = nil
				for _, ts := range tools {
					specsSeen = append(specsSeen, ts.Function.Name)
				}
				return Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID: "1", Type: "function",
						Function: FunctionCall{Name: "mcp_lookup", Arguments: `{}`},
					}},
				}, nil
			}
			for _, m := range messages {
				if m.Role == "tool" && strings.Contains(m.Content, "not in allowed tool set") {
					return Message{Role: "assistant", Content: "denied"}, nil
				}
			}
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	_, err = eng.PromptWith(t.Context(), "review", PromptOpts{
		ReadOnly:     true,
		Ephemeral:    true,
		AllowedTools: BuiltinReadInspectTools(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := BuiltinReadInspectTools()
	slices.Sort(specsSeen)
	slices.Sort(want)
	if !slices.Equal(specsSeen, want) {
		t.Fatalf("tool specs = %v want %v", specsSeen, want)
	}
	if extCalled != "" {
		t.Fatalf("extension tool executed despite allowlist: %q", extCalled)
	}
}

func TestPromptAllowedToolsExtraToolsCannotShadowBuiltin(t *testing.T) {
	var shadowCalled string
	var specsSeen []string
	turn := 0
	eng, err := New(Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			turn++
			if turn == 1 {
				specsSeen = nil
				for _, ts := range tools {
					specsSeen = append(specsSeen, ts.Function.Name)
				}
				return Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID: "1", Type: "function",
						Function: FunctionCall{Name: "read", Arguments: `{"path":"README.md"}`},
					}},
				}, nil
			}
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	_, err = eng.PromptWith(t.Context(), "read", PromptOpts{
		ReadOnly:     true,
		AllowedTools: BuiltinReadInspectTools(),
		ExtraTools: []Tool{&fakeTool{
			name:     "read",
			readOnly: true,
			got:      &shadowCalled,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Specs must list each allowlisted name once (engine builtin, not ExtraTools).
	counts := map[string]int{}
	for _, n := range specsSeen {
		counts[strings.ToLower(n)]++
	}
	if counts["read"] != 1 {
		t.Fatalf("read specs count = %d (%v)", counts["read"], specsSeen)
	}
	if shadowCalled != "" {
		t.Fatalf("ExtraTools shadowed builtin read: called %q", shadowCalled)
	}
}

func TestPromptAllowedToolsPermitsBuiltinRead(t *testing.T) {
	var called string
	turn := 0
	eng, err := New(Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			turn++
			if turn == 1 {
				return Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID: "1", Type: "function",
						Function: FunctionCall{Name: "read", Arguments: `{"path":"README.md"}`},
					}},
				}, nil
			}
			for _, m := range messages {
				if m.Role == "tool" && strings.Contains(m.Content, "denied") {
					t.Fatalf("read denied: %q", m.Content)
				}
				if m.Role == "tool" {
					called = m.Content
				}
			}
			return Message{Role: "assistant", Content: "done"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	_, err = eng.PromptWith(t.Context(), "read", PromptOpts{
		ReadOnly:     true,
		AllowedTools: BuiltinReadInspectTools(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if called == "" {
		t.Fatal("read tool was not executed")
	}
}
