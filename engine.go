package mow

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/contextload"
	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/policy"
	"github.com/subosito/mow/internal/session"
	"github.com/subosito/mow/internal/tools"
)

// Engine is the programmatic harness API: one agent, many prompts.
// Hosts never own the loop — use Prompt / OnEvent / Cancel.
//
//	eng, err := mow.New(mow.Options{...})
//	res, err := eng.Prompt(ctx, "list files")
//
// Package layout (same package mow): engine.go (New), engine_prompt.go,
// engine_model.go, engine_control.go, engine_adapt.go, run.go (Options/Run).
type Engine struct {
	// mu: short critical sections only — never hold across agent.Run / network.
	mu sync.Mutex
	// promptMu: serialize Prompt without blocking Model()/Wire() readers.
	promptMu sync.Mutex

	cfg        *config.File
	pol        *policy.Policy
	tools      []agent.Tool
	chat       agent.ChatFn
	client     *llm.Client // nil when Options.Provider/Chat is injected
	provider   Provider    // set when Options.Provider is used
	sys        string
	opt        Options
	sess       *session.Store
	sid        string
	prior      []llm.Message
	transcript []Message // user/assistant only (session resume)
	noSess     bool
	hooks      agent.Hooks
	life       lifeHooks
	// readOnlyExt marks ext tools that declared ReadOnly() true; only these
	// (plus builtin read tools) run under PromptOpts.ReadOnly.
	readOnlyExt map[string]bool

	onTokenMu   sync.Mutex
	onToken     func(string)
	onReasoning func(string)
	onEvents    []eventSub // fan-out; AddOnEvent / SetOnEvent
	nextEventID uint64

	runMu     sync.Mutex
	busy      bool
	runID     string
	runCancel context.CancelFunc
}

type eventSub struct {
	id uint64
	fn EventFunc
}

// lifeHooks are Engine-scoped (session start / user prompt / stop), not loop hooks.
type lifeHooks struct {
	onSessionStart []SessionStartFunc
	onUserPrompt   []UserPromptFunc
	onStop         []StopFunc
}

// New builds an Engine from Options (config, tools, optional session resume).
func New(opt Options) (*Engine, error) {
	// Packs that register config-driven tools (mcp, lsp) run before
	// construction; without this a library embedder that blank-imports a pack
	// would silently get none of its tools. Re-registration is safe —
	// ext.RegisterTool replaces by name.
	if err := ext.BeforeNew(opt.ConfigPaths...); err != nil {
		return nil, fmt.Errorf("extension init: %w", err)
	}
	cfg, err := config.Load(opt.ConfigPaths...)
	if err != nil {
		return nil, err
	}
	// Explicit Options overrides (do not mutate process env).
	if w := strings.TrimSpace(opt.Workspace); w != "" {
		cfg.Workspace = w
	}
	if m := strings.TrimSpace(opt.Model); m != "" {
		cfg.LLM.Model = m
	}
	if b := strings.TrimSpace(opt.BaseURL); b != "" {
		cfg.LLM.BaseURL = b
	}
	if opt.AllowWrite {
		cfg.Tools.Enable = appendUnique(cfg.Tools.Enable, "write", "edit")
	}
	if opt.AllowShell {
		cfg.Tools.Enable = appendUnique(cfg.Tools.Enable, "bash")
	}
	if opt.MaxTurns > 0 {
		cfg.Policy.MaxTurns = opt.MaxTurns
	}

	pol := &policy.Policy{
		Workspace:    cfg.Workspace,
		AllowWrite:   cfg.ToolEnabled("write") || cfg.ToolEnabled("edit"),
		AllowShell:   cfg.ToolEnabled("bash"),
		MaxReadBytes: cfg.Policy.MaxReadBytes,
		Hashline:     cfg.Tools.Hashline,
	}

	enabled := cfg.Tools.Enable
	toolList := tools.Registry(pol, enabled)
	readOnlyExt := map[string]bool{}
	for _, t := range ext.Tools() {
		toolList = append(toolList, adaptTool(t))
		// Ext tools may declare themselves side-effect free; only those run
		// in read-only prompts (see isReadOnlyTool).
		if ro, ok := t.(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
			readOnlyExt[strings.ToLower(strings.TrimSpace(t.Name()))] = true
		}
	}
	// Per-engine tools (Options.Tools): engine-scoped, unlike the global
	// registry. Same name overrides a registry tool; a builtin name is an
	// error — the jailed builtins must never be silently replaced.
	for _, t := range opt.Tools {
		if t == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(t.Name()))
		if name == "" {
			return nil, fmt.Errorf("options.tools: tool with empty name")
		}
		if isBuiltin(name) {
			return nil, fmt.Errorf("options.tools: %q collides with a builtin tool", name)
		}
		replaced := false
		for i, existing := range toolList {
			if strings.ToLower(strings.TrimSpace(existing.Name())) == name {
				toolList[i] = adaptTool(t)
				replaced = true
				break
			}
		}
		if !replaced {
			toolList = append(toolList, adaptTool(t))
		}
		if ro, ok := t.(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
			readOnlyExt[name] = true
		} else {
			delete(readOnlyExt, name) // an override may drop the marker
		}
	}

	sys, _ := contextload.Load(cfg.Workspace)
	skillDirs := append([]string(nil), cfg.Skills.Dirs...)
	if contextload.ProjectTrusted(cfg.Workspace) {
		skillDirs = append(skillDirs, filepath.Join(cfg.Workspace, ".mow", "skills"))
	} else if _, serr := os.Stat(filepath.Join(cfg.Workspace, ".mow")); serr == nil {
		slog.Info("mow: project .mow present but untrusted; run `mow trust` to load project config/skills")
	}
	skillDirs = append([]string{config.SkillsDir()}, skillDirs...)
	if sk := contextload.LoadSkills(skillDirs); sk != "" {
		if sys != "" {
			sys += "\n\n" + sk
		} else {
			sys = sk
		}
	}
	if s := strings.TrimSpace(opt.SystemAppend); s != "" {
		if sys != "" {
			sys += "\n\n" + s
		} else {
			sys = s
		}
	}
	if sys == "" {
		sys = "You are mow, a careful coding assistant. Prefer read-only tools unless write/shell are available. Stay within the workspace."
	}

	loopHooks, life := mergeHooks(opt.Hooks)

	e := &Engine{
		cfg:         cfg,
		pol:         pol,
		sys:         sys,
		opt:         opt,
		noSess:      opt.NoSession,
		hooks:       loopHooks,
		life:        life,
		onToken:     opt.OnToken,
		onReasoning: opt.OnReasoning,
		readOnlyExt: readOnlyExt,
	}
	if opt.OnEvent != nil {
		e.AddOnEvent(opt.OnEvent)
	}
	var mediaClient *llm.MediaClient
	switch {
	case opt.Provider != nil:
		prov := opt.Provider
		e.provider = prov
		e.chat = func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
			// Hooks are read per call so SetOnToken/Prompt wrappers apply to
			// custom providers exactly like the built-in client.
			e.onTokenMu.Lock()
			hooks := ChatHooks{OnToken: e.onToken, OnReasoning: e.onReasoning}
			e.onTokenMu.Unlock()
			out, err := prov.Chat(ctx, toPublicMessages(messages), toPublicToolSpecs(tools), hooks)
			if err != nil {
				return llm.Message{}, err
			}
			return toInternalMessage(out), nil
		}
		if key := cfg.ResolveAPIKey(); key != "" {
			mediaClient = &llm.MediaClient{
				BaseURL:      cfg.LLM.BaseURL,
				APIKey:       key,
				ExtraHeaders: withActorHeaders(cfg.LLM.Headers, "mow"),
			}
		}
	case opt.Chat != nil:
		e.chat = adaptChat(opt.Chat)
		if key := cfg.ResolveAPIKey(); key != "" {
			mediaClient = &llm.MediaClient{
				BaseURL:      cfg.LLM.BaseURL,
				APIKey:       key,
				ExtraHeaders: withActorHeaders(cfg.LLM.Headers, "mow"),
			}
		}
	default:
		key := cfg.ResolveAPIKey()
		if key == "" {
			return nil, fmt.Errorf("api key required (OPENAI_API_KEY / MOW_API_KEY / ANTHROPIC_API_KEY or llm.api_key)")
		}
		if strings.TrimSpace(cfg.LLM.Model) == "" {
			return nil, fmt.Errorf("model required (OPENAI_MODEL / MOW_MODEL / ANTHROPIC_MODEL or llm.model)")
		}
		headers := withActorHeaders(cfg.LLM.Headers, "mow")
		client := &llm.Client{
			Wire:         cfg.LLM.Wire,
			BaseURL:      cfg.LLM.BaseURL,
			APIKey:       key,
			Model:        cfg.LLM.Model,
			ExtraHeaders: headers,
			Stream:       cfg.LLM.Stream || opt.Stream,
		}
		e.client = client
		mediaClient = llm.FromClient(client)
		if client.ExtraHeaders[llm.HeaderComponent] == "" {
			client.ExtraHeaders[llm.HeaderComponent] = "turn.chat"
		}
		e.chat = func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
			e.onTokenMu.Lock()
			hooks := llm.StreamHooks{OnContent: e.onToken, OnReasoning: e.onReasoning}
			e.onTokenMu.Unlock()
			// Snapshot by value: SetModel/SetWire mutate e.client under e.mu
			// while a run may be in flight; the copy keeps this call race-free.
			e.mu.Lock()
			c := *e.client
			e.mu.Unlock()
			return c.ChatWithStream(ctx, messages, tools, hooks)
		}
	}

	if mediaClient != nil {
		toolList = append(toolList, tools.MediaTools(pol, tools.MediaOptions{
			Client:             mediaClient,
			GenerateImage:      cfg.LLM.Generate.Image,
			GenerateSpeech:     cfg.LLM.Generate.Speech,
			DefaultSpeechVoice: cfg.LLM.Generate.SpeechVoice,
			GenerateVideo:      cfg.LLM.Generate.Video,
			UnderstandImage:    cfg.LLM.Understand.Image,
			UnderstandVoice:    cfg.LLM.Understand.Voice,
			UnderstandVideo:    cfg.LLM.Understand.Video,
		})...)
	}

	enableSet := map[string]bool{}
	for _, name := range enabled {
		enableSet[strings.ToLower(name)] = true
	}
	var final []agent.Tool
	for _, t := range toolList {
		name := strings.ToLower(t.Name())
		// Builtins need tools.enable; registered ext tools are always included.
		if isBuiltin(name) && !enableSet[name] {
			continue
		}
		final = append(final, t)
	}
	for _, name := range []string{
		"generate_image", "generate_speech", "generate_video",
		"understand_image", "understand_voice", "understand_video",
	} {
		if !enableSet[name] {
			continue
		}
		if !toolPresent(final, name) {
			return nil, fmt.Errorf("tool %q enabled but llm.generate/understand model not set (or no API key for media)", name)
		}
	}
	e.tools = final

	if !opt.NoSession {
		proj := projectHash(cfg.Workspace)
		sessDir := filepath.Join(cfg.Session.Dir, proj)
		sid := strings.TrimSpace(opt.SessionID)
		if sid != "" {
			if err := session.ValidateID(sid); err != nil {
				return nil, err
			}
		}
		if sid == "" && opt.Continue {
			latest, err := session.LatestID(sessDir)
			if err != nil {
				return nil, fmt.Errorf("session continue: %w", err)
			}
			if latest != "" {
				sid = latest
			}
		}
		if sid == "" {
			sid = session.NewID()
		} else {
			store := &session.Store{Dir: sessDir, ID: sid}
			prior, err := store.LoadMessages()
			if err != nil {
				return nil, fmt.Errorf("session load: %w", err)
			}
			e.prior = prior
			turns, err := store.LoadTranscript()
			if err != nil {
				return nil, fmt.Errorf("session transcript: %w", err)
			}
			e.transcript = toPublicMessages(turns)
		}
		e.sid = sid
		e.sess = &session.Store{Dir: sessDir, ID: sid}
		if mediaClient != nil && sid != "" {
			if mediaClient.ExtraHeaders == nil {
				mediaClient.ExtraHeaders = map[string]string{}
			}
			if mediaClient.ExtraHeaders[llm.HeaderSession] == "" {
				mediaClient.ExtraHeaders[llm.HeaderSession] = sid
			}
		}
	}

	for _, fn := range e.life.onSessionStart {
		if fn == nil {
			continue
		}
		d, err := fn(context.Background(), SessionStartEvent{
			Workspace: e.cfg.Workspace,
			SessionID: e.sid,
			Model:     e.cfg.LLM.Model,
			System:    e.sys,
		})
		if err != nil {
			return nil, fmt.Errorf("session start hook: %w", err)
		}
		if s := strings.TrimSpace(d.SystemAppend); s != "" {
			if e.sys != "" {
				e.sys += "\n\n" + s
			} else {
				e.sys = s
			}
		}
	}

	return e, nil
}

// Extension decodes extensions.<name> from loaded config into dst.
// Missing section is a no-op. Hosts and packs decode their own keys.
func (e *Engine) Extension(name string, dst any) error {
	if e == nil || e.cfg == nil {
		return nil
	}
	return e.cfg.Extension(name, dst)
}

// Workspace returns the resolved workspace root.
func (e *Engine) Workspace() string {
	if e == nil || e.cfg == nil {
		return ""
	}
	return e.cfg.Workspace
}

// SessionID returns the active session id, if any.
func (e *Engine) SessionID() string {
	if e == nil {
		return ""
	}
	return e.sid
}

// Transcript returns user/assistant turns for UI display (session resume).
// Empty for a new session or NoSession. Excludes system prompts and tool dumps.
func (e *Engine) Transcript() []Message {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.transcript) == 0 {
		return nil
	}
	out := make([]Message, len(e.transcript))
	copy(out, e.transcript)
	return out
}

// Messages returns the full last agent-loop history (roles include tool), after
// the most recent Prompt. Used by hosts that need intermediate assistant prose
// (e.g. goal summary when the final line is only GOAL_DONE).
func (e *Engine) Messages() []Message {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.prior) == 0 {
		return nil
	}
	return toPublicMessages(e.prior)
}

// AllowWrite reports whether write/edit tools are enabled.
func (e *Engine) AllowWrite() bool {
	return e != nil && e.pol != nil && e.pol.AllowWrite
}

// AllowShell reports whether bash is enabled.
func (e *Engine) AllowShell() bool {
	return e != nil && e.pol != nil && e.pol.AllowShell
}

// AddPreTool appends a PreTool hook (deny / rewrite args / additional context).
// The returned unsubscribe detaches the hook (safe to call once, effective for
// in-flight runs too) — hosts like TUIs must detach on shutdown or a later
// headless Prompt would stall in an approval hook nobody answers.
func (e *Engine) AddPreTool(fn PreToolFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	h := adaptPreTool(fn)
	var off atomic.Bool
	wrapped := func(ctx context.Context, ev agent.PreToolEvent) (agent.PreToolDecision, error) {
		if off.Load() {
			return agent.PreToolDecision{}, nil
		}
		return h(ctx, ev)
	}
	e.mu.Lock()
	e.hooks.PreTool = append(e.hooks.PreTool, wrapped)
	e.mu.Unlock()
	return func() { off.Store(true) }
}

// AddAfterTurn appends a hook that fires after each LLM turn inside a Prompt
// (HasToolCalls reports whether a tool batch follows). UIs use it to commit
// intermediate assistant text at turn boundaries instead of losing it when
// the run ends. The returned unsubscribe detaches the hook.
func (e *Engine) AddAfterTurn(fn AfterTurnFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	var off atomic.Bool
	wrapped := func(ctx context.Context, ev agent.AfterTurnEvent) {
		if off.Load() {
			return
		}
		fn(ctx, AfterTurnEvent{AssistantText: ev.AssistantText, HasToolCalls: ev.HasToolCalls})
	}
	e.mu.Lock()
	e.hooks.AfterTurn = append(e.hooks.AfterTurn, wrapped)
	e.mu.Unlock()
	return func() { off.Store(true) }
}

// AddPostTool appends a PostTool hook (rewrite tool result shown to the model).
// The returned unsubscribe detaches the hook (safe to call once).
func (e *Engine) AddPostTool(fn PostToolFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	h := adaptPostTool(fn)
	var off atomic.Bool
	wrapped := func(ctx context.Context, ev agent.PostToolEvent) (agent.PostToolDecision, error) {
		if off.Load() {
			return agent.PostToolDecision{}, nil
		}
		return h(ctx, ev)
	}
	e.mu.Lock()
	e.hooks.PostTool = append(e.hooks.PostTool, wrapped)
	e.mu.Unlock()
	return func() { off.Store(true) }
}
