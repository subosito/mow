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
	"time"

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
	client     *llm.Client  // nil when Options.Provider/Chat is injected
	provider   Provider     // set when Options.Provider is used
	logger     *slog.Logger // nil → slog.Default()
	sys        string
	opt        Options
	sess       *session.Store
	sid        string
	prior      []llm.Message
	transcript []Message // user/assistant only (session resume)
	// lastCtxTokens is the most recent LLM call's input tokens ≈ current context
	// size (for a context-window fullness indicator). 0 until the first call.
	lastCtxTokens int
	// steer holds host guidance injected into the running turn at the next turn
	// boundary (Engine.Steer); drained by the loop, cleared at each run start.
	steer []string
	// cleanups run on Close (LIFO); closed guards against double-close.
	cleanups []func()
	closed   bool
	noSess   bool
	hooks    agent.Hooks
	life     lifeHooks
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
	if len(opt.ExtraRoots) > 0 {
		cfg.Policy.ExtraRoots = append(append([]string(nil), cfg.Policy.ExtraRoots...), opt.ExtraRoots...)
	}
	if m := strings.TrimSpace(opt.Model); m != "" {
		cfg.LLM.Model = m
	}
	if e := strings.TrimSpace(opt.Effort); e != "" {
		norm, nerr := llm.NormalizeEffort(e)
		if nerr != nil {
			return nil, fmt.Errorf("effort: %w", nerr)
		}
		cfg.LLM.Effort = norm
	}
	// Lean model id + effort: family-model-medium → model=family-model, effort=medium
	// when effort was not already set explicitly.
	if base, eff := llm.NormalizeConfiguredModel(cfg.LLM.Model, cfg.LLM.Effort); base != "" {
		cfg.LLM.Model = base
		cfg.LLM.Effort = eff
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
	// MaxTurns: >0 overrides; <0 means unlimited (stored as 0). 0 leaves config.
	if opt.MaxTurns > 0 {
		cfg.Policy.MaxTurns = opt.MaxTurns
	} else if opt.MaxTurns < 0 {
		cfg.Policy.MaxTurns = 0
	}

	pol := &policy.Policy{
		Workspace:      cfg.Workspace,
		ExtraRoots:     append([]string(nil), cfg.Policy.ExtraRoots...),
		AllowWrite:     cfg.ToolEnabled("write") || cfg.ToolEnabled("edit"),
		AllowShell:     cfg.ToolEnabled("bash"),
		MaxReadBytes:   cfg.Policy.MaxReadBytes,
		BashTimeoutSec: cfg.Policy.BashTimeoutSec,
		Hashline:       cfg.Tools.Hashline,
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

	// Compiled system: harness charter (always) + project AGENTS + skills + Options.SystemAppend.
	// llm.system_prefix is separate: prepended at request time (may set product identity).
	agents, _ := contextload.Load(cfg.Workspace)
	skillDirs := append([]string(nil), cfg.Skills.Dirs...)
	if contextload.ProjectTrusted(cfg.Workspace) {
		skillDirs = append(skillDirs, filepath.Join(cfg.Workspace, ".mow", "skills"))
	} else if _, serr := os.Stat(filepath.Join(cfg.Workspace, ".mow")); serr == nil {
		logger := opt.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Info("mow: project .mow present but untrusted; run `mow trust` to load project config/skills")
	}
	skillDirs = append([]string{config.SkillsDir()}, skillDirs...)
	skills := contextload.LoadSkills(skillDirs)
	sys := contextload.ComposeSystem(agents, skills, opt.SystemAppend)

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
		logger:      opt.Logger,
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
			if out.Usage.InputTokens > 0 {
				e.mu.Lock()
				e.lastCtxTokens = out.Usage.InputTokens
				e.mu.Unlock()
			}
			return toInternalMessage(out), nil
		}
		if key := cfg.ResolveAPIKey(); key != "" {
			mediaClient = &llm.MediaClient{
				BaseURL:      cfg.LLM.BaseURL,
				APIKey:       key,
				HTTP:         opt.HTTPClient,
				ExtraHeaders: withActorHeaders(cfg.LLM.Headers, "mow"),
			}
		}
	case opt.Chat != nil:
		inner := adaptChat(opt.Chat)
		e.chat = func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
			msg, err := inner(ctx, messages, tools)
			if err == nil && msg.Usage.InputTokens > 0 {
				e.mu.Lock()
				e.lastCtxTokens = msg.Usage.InputTokens
				e.mu.Unlock()
			}
			return msg, err
		}
		if key := cfg.ResolveAPIKey(); key != "" {
			mediaClient = &llm.MediaClient{
				BaseURL:      cfg.LLM.BaseURL,
				APIKey:       key,
				HTTP:         opt.HTTPClient,
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
			Effort:       cfg.LLM.Effort,
			HTTP:         opt.HTTPClient,
			ExtraHeaders: headers,
			Stream:       cfg.LLM.Stream || opt.Stream,
			// Prompt caching on by default (nil); explicit false disables it.
			PromptCache:        cfg.LLM.PromptCache == nil || *cfg.LLM.PromptCache,
			SystemPrefix:       append([]string(nil), cfg.LLM.SystemPrefix...),
			SystemPrefixModels: append([]string(nil), cfg.LLM.SystemPrefixModels...),
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
			msg, err := c.ChatWithStream(ctx, messages, tools, hooks)
			if err == nil && msg.Usage.InputTokens > 0 {
				e.mu.Lock()
				e.lastCtxTokens = msg.Usage.InputTokens // ≈ current context size
				e.mu.Unlock()
			}
			return msg, err
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

// ExtraRoots returns additional FS jail roots (copy). Empty when none configured.
func (e *Engine) ExtraRoots() []string {
	if e == nil || e.pol == nil || len(e.pol.ExtraRoots) == 0 {
		return nil
	}
	return append([]string(nil), e.pol.ExtraRoots...)
}

// ResolvePath applies the same path jail as FS tools: under Workspace or any
// ExtraRoot (symlink-safe). Relative paths join Workspace.
func (e *Engine) ResolvePath(rel string) (string, error) {
	if e == nil || e.pol == nil {
		return "", fmt.Errorf("mow: nil engine")
	}
	return e.pol.ResolvePath(rel)
}

// SessionID returns the active session id, if any.
func (e *Engine) SessionID() string {
	if e == nil {
		return ""
	}
	return e.sid
}

// SessionInfo summarizes a stored session (listing / resume UIs).
type SessionInfo struct {
	ID      string
	Updated time.Time
	Preview string // first user message
}

// Sessions lists stored sessions for this Engine's project, newest first.
// Empty when NoSession or none exist. Resuming a different id is out-of-band
// (build a new Engine with Options.SessionID).
func (e *Engine) Sessions() ([]SessionInfo, error) {
	if e == nil || e.sess == nil || strings.TrimSpace(e.sess.Dir) == "" {
		return nil, nil
	}
	infos, err := session.List(e.sess.Dir)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, len(infos))
	for i, s := range infos {
		out[i] = SessionInfo{ID: s.ID, Updated: s.Updated, Preview: s.Preview}
	}
	return out, nil
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

// Steer injects guidance into a running turn: the text is appended as a user
// message at the next turn boundary (after the current tool batch), so the model
// course-corrects without a cancel/restart. No-op-safe when idle — the message
// is delivered if a run consumes it before finishing, else dropped at the next
// run start. Safe to call from any goroutine.
func (e *Engine) Steer(text string) {
	if e == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	e.mu.Lock()
	e.steer = append(e.steer, text)
	e.mu.Unlock()
}

// drainSteer pops all pending steer messages (called by the loop at each turn
// boundary).
func (e *Engine) drainSteer() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.steer) == 0 {
		return nil
	}
	out := e.steer
	e.steer = nil
	return out
}

// Rewind drops the most recent user↔assistant exchange from the live context
// (in-memory history + transcript) and returns that user prompt. Use it to
// implement retry/edit: after Rewind, re-Prompt the returned text (or an edited
// version) and it replaces the removed turn. The next Prompt writes a corrected
// full-history snapshot, so a later resume is consistent; the append-only
// session file keeps the superseded turn but LoadMessages uses only the last
// snapshot. Returns ("", false) when there is nothing to rewind.
func (e *Engine) Rewind() (lastUser string, ok bool) {
	if e == nil {
		return "", false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// prior: trailing messages back through the last user prompt (tool results
	// have role "tool", so scanning for "user" lands on the real prompt).
	i := len(e.prior) - 1
	for i >= 0 && e.prior[i].Role != "user" {
		i--
	}
	if i < 0 {
		return "", false
	}
	lastUser = e.prior[i].Content
	e.prior = e.prior[:i]
	// transcript mirrors user/assistant turns only.
	j := len(e.transcript) - 1
	for j >= 0 && e.transcript[j].Role != "user" {
		j--
	}
	if j >= 0 {
		if lastUser == "" {
			lastUser = e.transcript[j].Content
		}
		e.transcript = e.transcript[:j]
	}
	e.lastCtxTokens = 0
	return lastUser, true
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
