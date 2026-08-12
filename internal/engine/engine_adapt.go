package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/config"
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

// Untrusted forwards optional Untrusted() from ext tools (acp, mcp, …).
func (a toolAdapter) Untrusted() bool {
	type u interface{ Untrusted() bool }
	if x, ok := a.t.(u); ok {
		return x.Untrusted()
	}
	return false
}

// ReadOnly forwards optional ReadOnly() so engine-scoped adapters match packs.
func (a toolAdapter) ReadOnly() bool {
	type r interface{ ReadOnly() bool }
	if x, ok := a.t.(r); ok {
		return x.ReadOnly()
	}
	return false
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
func mergeHooks(opt Options) (agent.Hooks, lifeHooks) {
	var h agent.Hooks
	var life lifeHooks
	hooks := opt.Hooks

	if !opt.DisableExtensionHooks {
		for _, fn := range ext.PreToolHooks() {
			fn := fn
			h.PreTool = append(h.PreTool, adaptPreToolExt(fn))
		}
	}
	for _, fn := range hooks.OnPreTool {
		fn := fn
		h.PreTool = append(h.PreTool, adaptPreTool(fn))
	}
	if !opt.DisableExtensionHooks {
		for _, fn := range ext.PostToolHooks() {
			fn := fn
			h.PostTool = append(h.PostTool, adaptPostToolExt(fn))
		}
	}
	for _, fn := range hooks.OnPostTool {
		fn := fn
		h.PostTool = append(h.PostTool, adaptPostTool(fn))
	}
	if !opt.DisableExtensionHooks {
		for _, fn := range ext.PreModelHooks() {
			fn := fn
			h.PreModel = append(h.PreModel, adaptPreModelExt(fn))
		}
		for _, fn := range ext.PreCompactHooks() {
			fn := fn
			h.PreCompact = append(h.PreCompact, adaptPreCompactExt(fn))
		}
	}
	for _, fn := range hooks.OnPreCompact {
		fn := fn
		h.PreCompact = append(h.PreCompact, adaptPreCompact(fn))
	}
	if !opt.DisableExtensionHooks {
		for _, fn := range ext.AfterTurnHooks() {
			fn := fn
			h.AfterTurn = append(h.AfterTurn, func(ctx context.Context, e agent.AfterTurnEvent) {
				fn(ctx, ext.AfterTurnEvent{AssistantText: e.AssistantText, HasToolCalls: e.HasToolCalls})
			})
		}
	}
	for _, fn := range hooks.OnAfterTurn {
		fn := fn
		h.AfterTurn = append(h.AfterTurn, func(ctx context.Context, e agent.AfterTurnEvent) {
			fn(ctx, AfterTurnEvent{AssistantText: e.AssistantText, HasToolCalls: e.HasToolCalls})
		})
	}

	if !opt.DisableExtensionHooks {
		for _, fn := range ext.SessionStartHooks() {
			fn := fn
			life.onSessionStart = append(life.onSessionStart, adaptSessionStartExt(fn))
		}
	}
	for _, fn := range hooks.OnSessionStart {
		fn := fn
		life.onSessionStart = append(life.onSessionStart, fn)
	}
	if !opt.DisableExtensionHooks {
		for _, fn := range ext.UserPromptHooks() {
			fn := fn
			life.onUserPrompt = append(life.onUserPrompt, adaptUserPromptExt(fn))
		}
	}
	for _, fn := range hooks.OnUserPrompt {
		fn := fn
		life.onUserPrompt = append(life.onUserPrompt, fn)
	}
	if !opt.DisableExtensionHooks {
		for _, fn := range ext.StopHooks() {
			fn := fn
			life.onStop = append(life.onStop, adaptStopExt(fn))
		}
	}
	for _, fn := range hooks.OnStop {
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
		d, err := fn(ctx, PreCompactEvent{
			EstChars: e.EstChars,
			MaxChars: e.MaxChars,
			Messages: toPublicMessages(e.Messages),
		})
		if err != nil {
			return agent.PreCompactDecision{}, err
		}
		return agent.PreCompactDecision{Skip: d.Skip, Summary: d.Summary}, nil
	}
}

func adaptPreModelExt(fn ext.PreModelFunc) agent.PreModelFunc {
	return func(ctx context.Context, e agent.PreModelEvent) (agent.PreModelDecision, error) {
		d, err := fn(ctx, ext.PreModelEvent{
			Turn:            e.Turn,
			InputTokens:     e.Usage.InputTokens,
			OutputTokens:    e.Usage.OutputTokens,
			SentChars:       e.SentChars,
			CharsPerToken:   e.CharsPerToken,
			MaxOutputTokens: e.MaxOutputTokens,
		})
		if err != nil {
			return agent.PreModelDecision{}, err
		}
		return agent.PreModelDecision{Stop: d.Stop, Reason: d.Reason}, nil
	}
}

func adaptPreCompactExt(fn ext.PreCompactFunc) agent.PreCompactFunc {
	return func(ctx context.Context, e agent.PreCompactEvent) (agent.PreCompactDecision, error) {
		d, err := fn(ctx, ext.PreCompactEvent{
			EstChars:     e.EstChars,
			MaxChars:     e.MaxChars,
			MessageCount: len(e.Messages),
		})
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
		Usage:      Usage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens, CachedInputTokens: m.Usage.CachedInputTokens, CacheWriteInputTokens: m.Usage.CacheWriteInputTokens},
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
		Usage:      llm.Usage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens, CachedInputTokens: m.Usage.CachedInputTokens, CacheWriteInputTokens: m.Usage.CacheWriteInputTokens},
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

// BuiltinReadInspectTools are the standard read-only inspection builtins.
// Strict review prompts should pass these as PromptOpts.AllowedTools together
// with ReadOnly so extension/MCP tools never appear in specs or execute.
func BuiltinReadInspectTools() []string { return []string{"read", "glob", "grep"} }

func allowedToolSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isAllowedTool(name string, allowed map[string]bool) bool {
	if len(allowed) == 0 {
		return true
	}
	return allowed[strings.ToLower(strings.TrimSpace(name))]
}

func filterAgentToolsByAllowed(tools []agent.Tool, allowed map[string]bool) []agent.Tool {
	if len(allowed) == 0 {
		return tools
	}
	// First match per lowercased name wins so PromptOpts.ExtraTools cannot
	// shadow engine builtins that share an allowlisted name (e.g. "read").
	seen := make(map[string]bool, len(allowed))
	out := make([]agent.Tool, 0, len(allowed))
	for _, t := range tools {
		if t == nil || !isAllowedTool(t.Name(), allowed) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(t.Name()))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
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

func removeNames(in []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, name := range names {
		drop[strings.ToLower(strings.TrimSpace(name))] = true
	}
	out := in[:0]
	for _, name := range in {
		if !drop[strings.ToLower(strings.TrimSpace(name))] {
			out = append(out, name)
		}
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

// otelCfgMap projects config.OTEL into the loose map the auto-wire hook reads.
func otelCfgMap(cfg *config.File) map[string]any {
	if cfg == nil || strings.TrimSpace(cfg.OTEL.Endpoint) == "" {
		return nil
	}
	m := map[string]any{
		"endpoint":     cfg.OTEL.Endpoint,
		"protocol":     cfg.OTEL.Protocol,
		"service_name": cfg.OTEL.ServiceName,
	}
	if len(cfg.OTEL.Headers) > 0 {
		m["headers"] = cfg.OTEL.Headers
	}
	return m
}

// cloneLLMClient snapshots mutable client state for one request/catalog call.
// Callers hold Engine.mu while cloning; maps and slices need deep copies because
// a shallow struct copy would still race with ListModels or SetModel.
func cloneLLMClient(src *llm.Client) llm.Client {
	if src == nil {
		return llm.Client{}
	}
	dst := *src
	dst.ExtraHeaders = cloneStringMap(src.ExtraHeaders)
	dst.SystemPrefix = append([]string(nil), src.SystemPrefix...)
	dst.SystemPrefixModels = append([]string(nil), src.SystemPrefixModels...)
	if src.CatalogModels != nil {
		dst.CatalogModels = make(map[string]llm.ModelInfo, len(src.CatalogModels))
		for id, info := range src.CatalogModels {
			info.Wires = append([]string(nil), info.Wires...)
			info.Efforts = append([]string(nil), info.Efforts...)
			info.NativeTools = append([]map[string]any(nil), info.NativeTools...)
			dst.CatalogModels[id] = info
		}
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
