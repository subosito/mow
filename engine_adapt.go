package mow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

type toolAdapter struct{ t ext.Tool }

func adaptTool(t ext.Tool) agent.Tool {
	if t == nil {
		return nil
	}
	return toolAdapter{t}
}

func (a toolAdapter) Name() string                { return a.t.Name() }
func (a toolAdapter) Description() string         { return a.t.Description() }
func (a toolAdapter) Parameters() json.RawMessage { return a.t.Parameters() }
func (a toolAdapter) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	return a.t.Exec(ctx, args)
}

func adaptChat(fn ChatFunc) agent.ChatFn {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		out, err := fn(ctx, toPublicMessages(messages), toPublicToolSpecs(tools))
		if err != nil {
			return llm.Message{}, err
		}
		return toInternalMessage(out), nil
	}
}

// mergeHooks combines ext globals + Options into agent loop hooks and engine life hooks.
// Order: ext globals first, then Options (so Options can override/annotate after packs).
func mergeHooks(opt Hooks) (agent.Hooks, lifeHooks) {
	var h agent.Hooks
	var life lifeHooks

	for _, fn := range ext.PreToolHooks() {
		fn := fn
		h.PreTool = append(h.PreTool, adaptPreToolExt(fn))
	}
	for _, fn := range opt.OnPreTool {
		fn := fn
		h.PreTool = append(h.PreTool, adaptPreTool(fn))
	}
	for _, fn := range ext.PostToolHooks() {
		fn := fn
		h.PostTool = append(h.PostTool, adaptPostToolExt(fn))
	}
	for _, fn := range opt.OnPostTool {
		fn := fn
		h.PostTool = append(h.PostTool, adaptPostTool(fn))
	}
	for _, fn := range ext.PreCompactHooks() {
		fn := fn
		h.PreCompact = append(h.PreCompact, adaptPreCompactExt(fn))
	}
	for _, fn := range opt.OnPreCompact {
		fn := fn
		h.PreCompact = append(h.PreCompact, adaptPreCompact(fn))
	}
	for _, fn := range ext.AfterTurnHooks() {
		fn := fn
		h.AfterTurn = append(h.AfterTurn, func(ctx context.Context, e agent.AfterTurnEvent) {
			fn(ctx, ext.AfterTurnEvent{AssistantText: e.AssistantText, HasToolCalls: e.HasToolCalls})
		})
	}
	for _, fn := range opt.OnAfterTurn {
		fn := fn
		h.AfterTurn = append(h.AfterTurn, func(ctx context.Context, e agent.AfterTurnEvent) {
			fn(ctx, AfterTurnEvent{AssistantText: e.AssistantText, HasToolCalls: e.HasToolCalls})
		})
	}

	for _, fn := range ext.SessionStartHooks() {
		fn := fn
		life.onSessionStart = append(life.onSessionStart, adaptSessionStartExt(fn))
	}
	for _, fn := range opt.OnSessionStart {
		fn := fn
		life.onSessionStart = append(life.onSessionStart, fn)
	}
	for _, fn := range ext.UserPromptHooks() {
		fn := fn
		life.onUserPrompt = append(life.onUserPrompt, adaptUserPromptExt(fn))
	}
	for _, fn := range opt.OnUserPrompt {
		fn := fn
		life.onUserPrompt = append(life.onUserPrompt, fn)
	}
	for _, fn := range ext.StopHooks() {
		fn := fn
		life.onStop = append(life.onStop, adaptStopExt(fn))
	}
	for _, fn := range opt.OnStop {
		fn := fn
		life.onStop = append(life.onStop, fn)
	}
	return h, life
}

func adaptPreTool(fn PreToolFunc) agent.PreToolFunc {
	return func(ctx context.Context, e agent.PreToolEvent) (agent.PreToolDecision, error) {
		d, err := fn(ctx, PreToolEvent{Name: e.Name, Args: e.Args, ToolCallID: e.ToolCallID})
		if err != nil {
			return agent.PreToolDecision{}, err
		}
		return agent.PreToolDecision{
			Deny: d.Deny, Message: d.Message, Args: d.Args,
			RewriteArgs: d.RewriteArgs, AdditionalContext: d.AdditionalContext,
		}, nil
	}
}

func adaptPreToolExt(fn ext.PreToolFunc) agent.PreToolFunc {
	return func(ctx context.Context, e agent.PreToolEvent) (agent.PreToolDecision, error) {
		d, err := fn(ctx, ext.PreToolEvent{Name: e.Name, Args: e.Args, ToolCallID: e.ToolCallID})
		if err != nil {
			return agent.PreToolDecision{}, err
		}
		return agent.PreToolDecision{
			Deny: d.Deny, Message: d.Message, Args: d.Args,
			RewriteArgs: d.RewriteArgs, AdditionalContext: d.AdditionalContext,
		}, nil
	}
}

func adaptPostTool(fn PostToolFunc) agent.PostToolFunc {
	return func(ctx context.Context, e agent.PostToolEvent) (agent.PostToolDecision, error) {
		d, err := fn(ctx, PostToolEvent{
			Name: e.Name, Args: e.Args, ToolCallID: e.ToolCallID,
			Result: e.Result, Denied: e.Denied, ExecErr: e.ExecErr,
			Duration: e.Duration,
		})
		if err != nil {
			return agent.PostToolDecision{}, err
		}
		return agent.PostToolDecision{Result: d.Result, Rewrite: d.Rewrite}, nil
	}
}

func adaptPostToolExt(fn ext.PostToolFunc) agent.PostToolFunc {
	return func(ctx context.Context, e agent.PostToolEvent) (agent.PostToolDecision, error) {
		d, err := fn(ctx, ext.PostToolEvent{
			Name: e.Name, Args: e.Args, ToolCallID: e.ToolCallID,
			Result: e.Result, Denied: e.Denied, ExecErr: e.ExecErr,
		})
		if err != nil {
			return agent.PostToolDecision{}, err
		}
		return agent.PostToolDecision{Result: d.Result, Rewrite: d.Rewrite}, nil
	}
}

func adaptPreCompact(fn PreCompactFunc) agent.PreCompactFunc {
	return func(ctx context.Context, e agent.PreCompactEvent) (agent.PreCompactDecision, error) {
		d, err := fn(ctx, PreCompactEvent{EstChars: e.EstChars, MaxChars: e.MaxChars})
		if err != nil {
			return agent.PreCompactDecision{}, err
		}
		return agent.PreCompactDecision{Skip: d.Skip, Summary: d.Summary}, nil
	}
}

func adaptPreCompactExt(fn ext.PreCompactFunc) agent.PreCompactFunc {
	return func(ctx context.Context, e agent.PreCompactEvent) (agent.PreCompactDecision, error) {
		d, err := fn(ctx, ext.PreCompactEvent{EstChars: e.EstChars, MaxChars: e.MaxChars})
		if err != nil {
			return agent.PreCompactDecision{}, err
		}
		return agent.PreCompactDecision{Skip: d.Skip, Summary: d.Summary}, nil
	}
}

func adaptSessionStartExt(fn ext.SessionStartFunc) SessionStartFunc {
	return func(ctx context.Context, e SessionStartEvent) (SessionStartDecision, error) {
		d, err := fn(ctx, ext.SessionStartEvent{
			Workspace: e.Workspace, SessionID: e.SessionID, Model: e.Model, System: e.System,
		})
		if err != nil {
			return SessionStartDecision{}, err
		}
		return SessionStartDecision{SystemAppend: d.SystemAppend}, nil
	}
}

func adaptUserPromptExt(fn ext.UserPromptFunc) UserPromptFunc {
	return func(ctx context.Context, e UserPromptEvent) (UserPromptDecision, error) {
		d, err := fn(ctx, ext.UserPromptEvent{
			Text: e.Text, SessionID: e.SessionID, Workspace: e.Workspace,
		})
		if err != nil {
			return UserPromptDecision{}, err
		}
		return UserPromptDecision{
			Text: d.Text, RewriteText: d.RewriteText, SystemAppend: d.SystemAppend,
		}, nil
	}
}

func adaptStopExt(fn ext.StopFunc) StopFunc {
	return func(ctx context.Context, e StopEvent) {
		fn(ctx, ext.StopEvent{Text: e.Text, Err: e.Err, SessionID: e.SessionID})
	}
}

func toPublicMessages(in []llm.Message) []Message {
	out := make([]Message, len(in))
	for i, m := range in {
		out[i] = toPublicMessage(m)
	}
	return out
}

func toPublicMessage(m llm.Message) Message {
	pm := Message{
		Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name,
		StopReason: m.StopReason,
		Usage:      Usage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens},
	}
	for _, tc := range m.ToolCalls {
		pm.ToolCalls = append(pm.ToolCalls, ToolCall{
			ID: tc.ID, Type: tc.Type,
			Function: FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return pm
}

func toInternalMessage(m Message) llm.Message {
	im := llm.Message{
		Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name,
		StopReason: m.StopReason,
		Usage:      llm.Usage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens},
	}
	for _, tc := range m.ToolCalls {
		im.ToolCalls = append(im.ToolCalls, llm.ToolCall{
			ID: tc.ID, Type: tc.Type,
			Function: llm.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return im
}

func toPublicToolSpecs(in []llm.ToolSpec) []ToolSpec {
	out := make([]ToolSpec, len(in))
	for i, t := range in {
		out[i] = ToolSpec{
			Type: t.Type,
			Function: ToolSpecFunction{
				Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters,
			},
		}
	}
	return out
}

func isBuiltin(name string) bool {
	switch name {
	case "read", "glob", "grep", "write", "edit", "bash",
		"generate_image", "generate_speech", "generate_video",
		"understand_image", "understand_voice", "understand_video":
		return true
	default:
		return false
	}
}

// isReadOnlyTool reports whether a tool may run in a read-only prompt:
// builtin read tools, understand_* (side-effect free), and ext tools that
// declared ReadOnly() true at registration. generate_* writes media files and
// is excluded.
func isReadOnlyTool(name string, extRO map[string]bool) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "read", "glob", "grep",
		"understand_image", "understand_voice", "understand_video":
		return true
	}
	return extRO[n]
}

func toolPresent(list []agent.Tool, name string) bool {
	for _, t := range list {
		if strings.EqualFold(t.Name(), name) {
			return true
		}
	}
	return false
}

func withActorHeaders(in map[string]string, actor string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	if actor != "" && out[llm.HeaderActor] == "" {
		out[llm.HeaderActor] = actor
	}
	return out
}

func appendUnique(list []string, names ...string) []string {
	set := map[string]bool{}
	for _, l := range list {
		set[l] = true
	}
	for _, n := range names {
		if !set[n] {
			list = append(list, n)
			set[n] = true
		}
	}
	return list
}

func projectHash(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(sum[:8])
}
