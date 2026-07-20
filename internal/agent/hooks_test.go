package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

func TestPreToolRewriteAndPostToolRewrite(t *testing.T) {
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "1", Type: "function",
					Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"orig"}`},
				}},
			}, nil
		}
		var toolContent string
		for _, m := range messages {
			if m.Role == "tool" {
				toolContent = m.Content
			}
		}
		if !strings.Contains(toolContent, "rewritten") {
			t.Fatalf("tool content=%q want rewritten", toolContent)
		}
		if !strings.Contains(toolContent, "hint:") {
			t.Fatalf("tool content=%q want additional context", toolContent)
		}
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		Tools:    []agent.Tool{echoTool{}},
		Hooks: agent.Hooks{
			PreTool: []agent.PreToolFunc{
				func(ctx context.Context, e agent.PreToolEvent) (agent.PreToolDecision, error) {
					return agent.PreToolDecision{
						RewriteArgs:       true,
						Args:              json.RawMessage(`{"text":"rewritten"}`),
						AdditionalContext: "hint: use carefully",
					}, nil
				},
			},
			PostTool: []agent.PostToolFunc{
				func(ctx context.Context, e agent.PostToolEvent) (agent.PostToolDecision, error) {
					return agent.PostToolDecision{
						Rewrite: true,
						Result:  e.Result + "\n(post)",
					}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" {
		t.Fatalf("text=%q", res.Text)
	}
	// Confirm post rewrite landed
	found := false
	for _, m := range res.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "(post)") {
			found = true
		}
	}
	if !found {
		t.Fatal("post-tool rewrite missing")
	}
}

func TestPreToolDeny(t *testing.T) {
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "1", Type: "function",
					Function: llm.FunctionCall{Name: "echo", Arguments: `{}`},
				}},
			}, nil
		}
		for _, m := range messages {
			if m.Role == "tool" && strings.Contains(m.Content, "blocked") {
				return llm.Message{Role: "assistant", Content: "denied-ok"}, nil
			}
		}
		return llm.Message{Role: "assistant", Content: "fail"}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		Tools:    []agent.Tool{echoTool{}},
		Hooks: agent.Hooks{
			PreTool: []agent.PreToolFunc{
				func(ctx context.Context, e agent.PreToolEvent) (agent.PreToolDecision, error) {
					return agent.PreToolDecision{Deny: true, Message: "blocked"}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "denied-ok" {
		t.Fatalf("text=%q", res.Text)
	}
}

func TestPreCompactSummary(t *testing.T) {
	// Force compaction with a tiny budget and many prior messages.
	var sawSummary bool
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		for _, m := range messages {
			if strings.Contains(m.Content, "CUSTOM_SUMMARY") {
				sawSummary = true
			}
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	var prior []llm.Message
	prior = append(prior, llm.Message{Role: "system", Content: "sys"})
	for i := 0; i < 30; i++ {
		prior = append(prior, llm.Message{Role: "user", Content: strings.Repeat("x", 80)})
		prior = append(prior, llm.Message{Role: "assistant", Content: strings.Repeat("y", 80)})
	}
	_, err := agent.Run(context.Background(), chat, "next", agent.Options{
		System:          "sys",
		PriorMessages:   prior,
		MaxContextChars: 500,
		Hooks: agent.Hooks{
			PreCompact: []agent.PreCompactFunc{
				func(ctx context.Context, e agent.PreCompactEvent) (agent.PreCompactDecision, error) {
					return agent.PreCompactDecision{Summary: "CUSTOM_SUMMARY"}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSummary {
		t.Fatal("expected custom compact summary in LLM messages")
	}
}

func TestAfterTurnSeesToolCalls(t *testing.T) {
	var turns []agent.AfterTurnEvent
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "1", Type: "function",
					Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"a"}`},
				}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "final"}, nil
	}
	_, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		Tools:    []agent.Tool{echoTool{}},
		Hooks: agent.Hooks{
			AfterTurn: []agent.AfterTurnFunc{
				func(ctx context.Context, e agent.AfterTurnEvent) {
					turns = append(turns, e)
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns=%d want 2", len(turns))
	}
	if !turns[0].HasToolCalls || turns[1].HasToolCalls {
		t.Fatalf("turns=%+v", turns)
	}
	if turns[1].AssistantText != "final" {
		t.Fatalf("final text=%q", turns[1].AssistantText)
	}
}
