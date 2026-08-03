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
	// steerCancel cancels the in-flight run's LLM call so a mid-turn steer
	// interrupts immediately (the loop reissues with the steer appended).
	steerCancel context.CancelFunc
	// promptMu: serialize Prompt without blocking Model()/Wire() readers.
	promptMu sync.Mutex

	cfg          *config.File
	pol          *policy.Policy
	tools        []agent.Tool
	chat         agent.ChatFn
	client       *llm.Client  // nil when Options.Provider/Chat is injected
	provider     Provider     // set when Options.Provider is used
	logger       *slog.Logger // nil → slog.Default()
	sys          string
	agents       string
	skillDirs    []string
	skillSelect  bool
	skillsLoaded bool
	opt          Options
	sess         *session.Store
	sid          string
	prior        []llm.Message
	transcript   []Message // user/assistant only (session resume)
	// lastCtxTokens is the most recent LLM call's input tokens ≈ current context
	// size (for a context-window fullness indicator). 0 until the first call.
	lastCtxTokens int
	// wireExplicit: user pinned wire (yaml/env/SetWire). When false, catalog
	// preferred wire for the active model is applied after ListModels / SetModel.
	wireExplicit bool
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
	if len(opt.ExtraRootsReadOnly) > 0 {
		cfg.Policy.ExtraRootsReadOnly = append(append([]string(nil), cfg.Policy.ExtraRootsReadOnly...), opt.ExtraRootsReadOnly...)
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
	if len(opt.SystemPrefix) > 0 {
		cfg.LLM.SystemPrefix = append([]string(nil), opt.SystemPrefix...)
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
		Workspace:          cfg.Workspace,
		ExtraRoots:         append([]string(nil), cfg.Policy.ExtraRoots...),
		ExtraRootsReadOnly: append([]string(nil), cfg.Policy.ExtraRootsReadOnly...),
		AllowWrite:         cfg.ToolEnabled("write") || cfg.ToolEnabled("edit"),
		AllowShell:         cfg.ToolEnabled("bash"),
		MaxReadBytes:       cfg.Policy.MaxReadBytes,
		BashTimeoutSec:     cfg.Policy.BashTimeoutSec,
		MaxBashTimeoutSec:  cfg.Policy.MaxBashTimeoutSec,
		Hashline:           cfg.Tools.Hashline,
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
		// The trust cue is a user-facing hint, not log noise: emit it as a
		// plain stderr line unless the host configured its own logger.
		msg := "mow: project .mow/ found but not trusted — run `mow trust` to load its config/skills"
		if opt.Logger != nil {
			opt.Logger.Warn(msg)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
	}
	skillDirs = append([]string{config.SkillsDir()}, skillDirs...)
	selectSkills := true
	if cfg.Skills.Selector != nil {
		selectSkills = *cfg.Skills.Selector
	}
	skills := ""
	if !selectSkills {
		skills = contextload.LoadSkills(skillDirs)
	}
	// Concrete workspace + extra roots so the model does not refuse --extra-root
	// paths as "restricted" (policy already allows them; instructions must match).
	jailFacts := contextload.PathJailFacts(cfg.Workspace, pol.ExtraRoots, pol.ExtraRootsReadOnly)
	sys := contextload.ComposeSystem(jailFacts, agents, skills, opt.SystemAppend)

	loopHooks, life := mergeHooks(opt.Hooks)

	e := &Engine{
		cfg:          cfg,
		pol:          pol,
		sys:          sys,
		agents:       agents,
		skillDirs:    append([]string(nil), skillDirs...),
		skillSelect:  selectSkills,
		skillsLoaded: !selectSkills,
		opt:          opt,
		noSess:       opt.NoSession,
		hooks:        loopHooks,
		life:         life,
		onToken:      opt.OnToken,
		onReasoning:  opt.OnReasoning,
		readOnlyExt:  readOnlyExt,
		logger:       opt.Logger,
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
		if e.cfg != nil && strings.TrimSpace(opt.Model) != "" {
			e.cfg.LLM.Model = strings.TrimSpace(opt.Model)
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
			return nil, fmt.Errorf("api key required (OPENAI_API_KEY / MOW_API_KEY / ANTHROPIC_API_KEY, or llm.api_key in the config file under MOW_HOME)")
		}
		if strings.TrimSpace(cfg.LLM.Model) == "" && !opt.Continue && strings.TrimSpace(opt.SessionID) == "" {
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
		e.wireExplicit = cfg.LLM.WireExplicit
		mediaClient = llm.FromClient(client)
		if client.ExtraHeaders[llm.HeaderComponent] == "" {
			client.ExtraHeaders[llm.HeaderComponent] = "turn.chat"
		}
		// Fetch GET /v1/models for Limits() (window/pricing) and to align wire
		// with the catalog preferred protocol for this model (e.g. --model
		// claude-* → anthropic-messages) when the user did not pin llm.wire.
		// Sync so the first Prompt is not still on openai-chat-completions
		// while a gateway O2A adapter drops Anthropic prompt cache.
		{
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = client.ListModels(ctx)
			cancel()
			e.applyPreferredWireFromCatalog()
		}
		e.chat = func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
			e.onTokenMu.Lock()
			hooks := llm.StreamHooks{OnContent: e.onToken, OnReasoning: e.onReasoning}
			e.onTokenMu.Unlock()
			// Snapshot all mutable request/catalog state under e.mu. ListModels
			// publishes refreshed catalogs under the same lock, so a running call
			// never shares maps or slices with model switching/catalog refresh.
			e.mu.Lock()
			c := cloneLLMClient(e.client)
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
			// Re-validate: session dir listings can include non-id names if a
			// file was planted; never resume an id that fails ValidateID.
			if latest != "" {
				if err := session.ValidateID(latest); err != nil {
					return nil, fmt.Errorf("session continue: %w", err)
				}
				sid = latest
			}
		}
		if sid == "" {
			sid = session.NewID()
		} else {
			if err := session.ValidateID(sid); err != nil {
				return nil, err
			}
			store := &session.Store{Dir: sessDir, ID: sid}
			// Resume the model last used by this session. Model state is loaded
			// after the client/catalog exists so effort and preferred wire remain
			// synchronized through the normal SetModel path. Legacy sessions have
			// no runtime event and keep the configured default.
			runtime, rerr := store.LoadRuntime()
			if rerr != nil {
				return nil, fmt.Errorf("session runtime load: %w", rerr)
			}
			if runtime.Model != "" && !opt.ExplicitModel {
				// Only --model / ExplicitModel wins over the session runtime. Config
				// and env values are defaults and therefore yield on resume. Restore
				// the recorded wire too: otherwise SetModel may infer a different wire
				// from today's catalog/default than the session actually used.
				if e.client != nil || e.provider != nil {
					if err := e.SetModel(runtime.Model); err != nil {
						return nil, fmt.Errorf("session model restore: %w", err)
					}
					if runtime.Wire != "" && e.client != nil {
						if err := e.SetWire(runtime.Wire); err != nil {
							return nil, fmt.Errorf("session wire restore: %w", err)
						}
					}
				} else if e.cfg != nil {
					e.cfg.LLM.Model = runtime.Model
				}
			}
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
	dst.CatalogIDs = append([]string(nil), src.CatalogIDs...)
	if src.CatalogModels != nil {
		dst.CatalogModels = make(map[string]llm.ModelInfo, len(src.CatalogModels))
		for id, info := range src.CatalogModels {
			info.Wires = append([]string(nil), info.Wires...)
			info.Efforts = append([]string(nil), info.Efforts...)
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

// ExtraRootsReadOnly returns additional read-only FS jail roots (copy).
func (e *Engine) ExtraRootsReadOnly() []string {
	if e == nil || e.pol == nil || len(e.pol.ExtraRootsReadOnly) == 0 {
		return nil
	}
	return append([]string(nil), e.pol.ExtraRootsReadOnly...)
}

// ResolvePath applies the same path jail as FS tools: under Workspace or any
// ExtraRoot (symlink-safe). Relative paths join Workspace.
func (e *Engine) ResolvePath(rel string) (string, error) {
	if e == nil || e.pol == nil {
		return "", fmt.Errorf("mow: nil engine")
	}
	return e.pol.ResolvePath(rel)
}

// ResolvePathFor applies the path jail for read or write context.
func (e *Engine) ResolvePathFor(rel string, write bool) (string, error) {
	if e == nil || e.pol == nil {
		return "", fmt.Errorf("mow: nil engine")
	}
	return e.pol.ResolvePathFor(rel, write)
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
	cancel := e.steerCancel
	e.mu.Unlock()
	// Emit before cancelling so hosts reset their live stream first; then
	// interrupt the in-flight LLM call — the loop reissues with the steer
	// appended as a user message (no cancel/restart, no lost work).
	e.Emit(Event{Type: EventSteer, Delta: text})
	if cancel != nil {
		cancel()
	}
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

// CompactReport summarizes a manual compaction (Engine.Compact).
type CompactReport struct {
	// Layer is the compaction layer used ("snip" or "drop").
	Layer string `json:"layer,omitempty"`
	// CharsSaved is the raw character reduction (before − after).
	CharsSaved int `json:"chars_saved,omitempty"`
	// MessagesBefore/After are the transcript sizes around the compaction.
	MessagesBefore int `json:"messages_before,omitempty"`
	MessagesAfter  int `json:"messages_after,omitempty"`
	// OverBudget is true when even drop+summarize could not reach the target.
	OverBudget bool `json:"over_budget,omitempty"`
}

// Compact manually compacts the engine's stored transcript (the context the
// next Prompt resumes with) using the same tiered machinery as the loop's
// automatic compaction: snip bulky tool results first, then drop+summarize
// old turns with task anchors. Raw session JSONL is never touched — this only
// rewrites the in-memory projection. maxChars <= 0 uses the default budget.
// Emits a loop.compact event. No-op (empty report) when there is no history.
func (e *Engine) Compact(maxChars int) (CompactReport, error) {
	if e == nil {
		return CompactReport{}, fmt.Errorf("engine: nil")
	}
	// The NEXT prompt's history is e.prior (the previous run's full message
	// list), NOT e.transcript (UI user/assistant turns only). Compacting the
	// wrong one made /compact a visual no-op on the wire — compact prior.
	e.mu.Lock()
	prior := append([]llm.Message(nil), e.prior...)
	if maxChars <= 0 {
		configured := 0
		ratio := agent.DefaultCompactRatio
		if e.cfg != nil {
			configured = e.cfg.Policy.MaxContextChars
			if e.cfg.Policy.CompactRatio > 0 {
				ratio = e.cfg.Policy.CompactRatio
			}
		}
		if e.opt.MaxContextChars > 0 {
			configured = e.opt.MaxContextChars
		}
		maxChars = resolveMaxContextChars(configured, e.limitsLocked().ContextWindow, ratio)
		if maxChars <= 0 {
			// Manual compaction is an explicit request even when automatic
			// compaction was disabled with max_context_chars: -1.
			maxChars = agent.DefaultMaxContextChars
		}
	}
	e.mu.Unlock()
	if len(prior) == 0 {
		return CompactReport{}, nil
	}
	res := agent.CompactTiered(prior, maxChars, "", agent.DefaultMaxToolResultChars)
	if res.Messages == nil {
		return CompactReport{}, fmt.Errorf("engine: compact failed (nil result)")
	}
	e.mu.Lock()
	e.prior = res.Messages
	// Keep the UI transcript aligned with the compacted history (user/assistant
	// roles only, mirroring how run end appends to it).
	var t []Message
	for _, m := range res.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			t = append(t, toPublicMessage(m))
		}
	}
	e.transcript = t
	e.mu.Unlock()

	rep := CompactReport{
		Layer:          string(res.Layer),
		CharsSaved:     res.CharsSaved,
		MessagesBefore: res.MessagesBefore,
		MessagesAfter:  res.MessagesAfter,
		OverBudget:     res.OverBudget,
	}
	e.Emit(Event{
		Type:           EventCompact,
		Layer:          CompactLayer(res.Layer),
		CharsBefore:    res.CharsBefore,
		CharsAfter:     res.CharsAfter,
		CharsSaved:     res.CharsSaved,
		MessagesBefore: res.MessagesBefore,
		MessagesAfter:  res.MessagesAfter,
		OverBudget:     res.OverBudget,
	})
	return rep, nil
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
	// prior: trailing messages back through the last REAL user prompt. Tool
	// results have role "tool", and host-injected nudges (thrash/explore
	// warnings, mid-turn steer) are marked Synthetic — skip both so edit/retry
	// lands on the user's own prompt, never a warning or steer string.
	i := len(e.prior) - 1
	for i >= 0 && (e.prior[i].Role != "user" || e.prior[i].Synthetic) {
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
