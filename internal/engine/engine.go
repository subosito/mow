package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

	cfg            *config.File
	pol            *policy.Policy
	tools          []agent.Tool
	chat           agent.ChatFn
	client         *llm.Client  // nil when Options.Provider/Chat is injected
	provider       Provider     // set when Options.Provider is used
	logger         *slog.Logger // nil → slog.Default()
	sys            string
	agents         string
	skillDirs      []string
	skillSelect    bool
	skillsLoaded   bool
	skillsText     string // cumulative loaded skill markdown (explicit + prompt-matched + activated)
	explicitSkills []string
	opt            Options
	sess           *session.Store
	sid            string
	prior          []llm.Message
	transcript     []Message // user/assistant only (session resume)
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
	readOnlyExt    map[string]bool
	untrustedNonce string

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
	// --workspace / Options.Workspace is hybrid: first it is looked up as a
	// named set in $MOW_HOME/workspaces.yaml (root + extra_roots); otherwise
	// it is treated as a plain directory path. A matched set wins, and its
	// extra_roots are prepended to policy.extra_roots — explicit --extra-root
	// flags still append on top.
	if w := strings.TrimSpace(opt.Workspace); w != "" {
		set, names, found, werr := config.LookupWorkspaceSet(w)
		if werr != nil {
			return nil, werr
		}
		if found {
			ws, roots, rerr := set.ResolveWorkspaceSet()
			if rerr != nil {
				return nil, rerr
			}
			var rw, ro []string
			for _, r := range roots {
				path, readOnly := config.SplitExtraRootSpec(r)
				if path == "" {
					continue
				}
				if readOnly {
					ro = append(ro, path)
				} else {
					rw = append(rw, path)
				}
			}
			cfg.Workspace = ws
			cfg.Policy.ExtraRoots = append(rw, cfg.Policy.ExtraRoots...)
			cfg.Policy.ExtraRootsReadOnly = append(ro, cfg.Policy.ExtraRootsReadOnly...)
		} else {
			// Not a set name. If it is also not a usable directory and sets
			// are defined, this is likely a set-name typo — list them instead
			// of silently jail-rooting a bogus path.
			if fi, serr := os.Stat(w); serr != nil || !fi.IsDir() {
				if len(names) > 0 {
					return nil, fmt.Errorf("workspace %q is not a directory and no workspace set has that name (defined sets: %s)", w, strings.Join(names, ", "))
				}
			}
			cfg.Workspace = w
		}
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
	if opt.DisableWrite {
		cfg.Tools.Enable = removeNames(cfg.Tools.Enable, "write", "edit")
	}
	if opt.DisableShell {
		cfg.Tools.Enable = removeNames(cfg.Tools.Enable, "bash")
	}
	// --read-only with at least one --extra-root PATH:rw keeps write/edit
	// tools available (so writes can land under the explicit writable roots)
	// while the policy jail makes the workspace and unsuffixed extra roots
	// read-only. Without any writable root, --read-only is a pure disable and
	// leaves write/edit removed (backward compatible).
	if opt.ReadOnly && len(opt.WritableRoots) > 0 {
		cfg.Tools.Enable = appendUnique(cfg.Tools.Enable, "write", "edit")
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
		WritableRoots:      append([]string(nil), opt.WritableRoots...),
		ReadOnly:           opt.ReadOnly,
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
	// Programmatic dirs (tests/hosts) are searched last, after global/config
	// dirs, so config and the global $MOW_HOME/skills still take precedence.
	skillDirs = append(skillDirs, opt.SkillsDirs...)
	selectSkills := true
	if cfg.Skills.Selector != nil {
		selectSkills = *cfg.Skills.Selector
	}
	// Explicit skills merge: CLI/options --skill + config skills.explicit.
	explicitSkills := mergeSkillNames(
		cfg.Skills.Explicit,
		opt.ExplicitSkills,
	)
	skills := ""
	if !selectSkills {
		skills = contextload.LoadSkills(skillDirs)
	}
	// When the selector is on and explicit skills are set, do NOT compile the
	// system prompt here: the first-prompt selector must also run so
	// prompt-matched skills merge with explicit ones. Defer to the lazy load
	// in PromptWith. Explicit-only (selector off) compiles now.
	// Concrete workspace + extra roots so the model does not refuse --extra-root
	// paths as "restricted" (policy already allows them; instructions must match).
	jailFacts := contextload.PathJailFacts(cfg.Workspace, pol.ExtraRoots, pol.ExtraRootsReadOnly)
	sys := contextload.ComposeSystem(jailFacts, agents, skills, opt.SystemAppend)

	loopHooks, life := mergeHooks(opt.Hooks)

	e := &Engine{
		cfg:            cfg,
		pol:            pol,
		sys:            sys,
		agents:         agents,
		skillDirs:      append([]string(nil), skillDirs...),
		skillSelect:    selectSkills,
		skillsLoaded:   !selectSkills,
		skillsText:     skills,
		explicitSkills: explicitSkills,
		opt:            opt,
		noSess:         opt.NoSession,
		hooks:          loopHooks,
		life:           life,
		onToken:        opt.OnToken,
		onReasoning:    opt.OnReasoning,
		readOnlyExt:    readOnlyExt,
		logger:         opt.Logger,
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
			EffortPinned: strings.TrimSpace(cfg.LLM.Effort) != "",
			HTTP:         opt.HTTPClient,
			ExtraHeaders: headers,
			Stream:       cfg.LLM.Stream || opt.Stream,
			// Prompt caching on by default (nil); "none"/false disables it.
			PromptCache:        cfg.LLM.PromptCache == nil || cfg.LLM.PromptCache.Enabled(),
			CacheTTL:           cacheTTL(cfg.LLM.PromptCache),
			MaxTokens:          cfg.LLM.MaxTokens,
			NativeTools:        cfg.LLM.NativeTools,
			SystemPrefix:       append([]string(nil), cfg.LLM.SystemPrefix...),
			SystemPrefixModels: append([]string(nil), cfg.LLM.SystemPrefixModels...),
			FirstByteTimeout:   time.Duration(cfg.LLM.FirstByteTimeoutSec) * time.Second,
			CallTimeout:        time.Duration(cfg.LLM.CallTimeoutSec) * time.Second,
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
			// The model may have come from --model before the catalog was
			// available. Apply its advertised default_effort now, just as
			// SetModel does for subsequent switches.
			client.SyncEffortToModel(client.Model)
			cfg.LLM.Effort = client.Effort
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
		// Optional extension tools may make availability engine/config-specific.
		if conditional, ok := t.(interface{ Enabled(*Engine) bool }); ok && !conditional.Enabled(e) {
			continue
		}
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
	// Per-engine nonce for untrusted-output framing (bash/MCP/delegate).
	nb := make([]byte, 8)
	if _, err := rand.Read(nb); err == nil {
		e.untrustedNonce = hex.EncodeToString(nb)
	}

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
			if runtime.Effort != "" && !opt.ExplicitEffort && !opt.ExplicitModel {
				// Restore the effort last used by this session with the same
				// precedence as the model: only --effort wins over it. Skipped
				// when the model is explicit — the caller pinned a model whose
				// catalog efforts may not include the session's stored tier,
				// and SetModel already synced effort for that model.
				e.mu.Lock()
				var allowed []string
				if e.client != nil {
					allowed = e.client.EffortsForModel(e.client.Model)
				}
				if norm, nerr := llm.NormalizeEffortFor(runtime.Effort, allowed); nerr == nil {
					if e.client != nil {
						e.client.Effort = norm
					}
					if e.cfg != nil {
						e.cfg.LLM.Effort = norm
					}
				}
				e.mu.Unlock()
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
		// context_search (recovery for compaction archives and stored tool
		// results) is provided by the optional packs/contextsink pack, which
		// registers it via ext.RegisterTool; the tool resolves the session
		// dir from the engine at call time.
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

	// Optional OTLP export when otel.endpoint is set and the otel package
	// registered an auto-wire hook (stock CLI blank-imports it). Empty
	// endpoint → no-op; core stays free of the OTLP SDK import.
	if err := runOTELAuto(e, otelCfgMap(e.cfg)); err != nil {
		return nil, err
	}

	// Fail closed on an unenforceable spend ceiling. policy.max_run_usd on a
	// model with no published price cannot fire; starting anyway would hand
	// the operator a limit that silently never triggers, which is worse than
	// having none. Refuse at construction, where the message is actionable.
	if _, err := e.budgetGate(); err != nil {
		return nil, err
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

// cacheTTL maps the configured prompt-cache mode to a wire ttl. Unset means
// the provider default.
func cacheTTL(m *config.CacheMode) string {
	if m == nil {
		return ""
	}
	return m.TTL()
}

// mergeSkillNames unions two skill-name lists case-insensitively, dedupes,
// and preserves first-seen order (so CLI --skill names appear before config
// names in the merged list — useful for diagnostics, not semantics).
func mergeSkillNames(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// mergeSkillText concatenates two skill-text blobs (each a sequence of
// "## skill: <name>" sections) and dedupes by skill label, preserving the
// order from a then b. A skill present in both prompt-matched and explicit
// sets appears once (first occurrence wins).
func mergeSkillText(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	seen := map[string]bool{}
	var out []string
	for _, blob := range []string{a, b} {
		for _, sec := range strings.Split(blob, "\n\n") {
			sec = strings.TrimSpace(sec)
			if sec == "" {
				continue
			}
			label := skillSectionLabel(sec)
			if label != "" && seen[label] {
				continue
			}
			if label != "" {
				seen[label] = true
			}
			out = append(out, sec)
		}
	}
	return strings.Join(out, "\n\n")
}

// skillSectionLabel extracts the skill folder name from a "## skill: <name>"
// section header (case-insensitive match on the prefix). Empty when the
// section does not start with the skill marker.
func skillSectionLabel(sec string) string {
	sec = strings.TrimSpace(sec)
	if !strings.HasPrefix(sec, "## skill:") {
		return ""
	}
	rest := strings.TrimPrefix(sec, "## skill:")
	rest = strings.TrimSpace(rest)
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.ToLower(strings.TrimSpace(rest))
}

// recomposeSystemLocked rebuilds e.sys from the current e.skillsText and the
// other immutable system segments (jail facts, agents framing, SystemAppend).
// Caller must hold e.mu. Returns the composed system string.
func (e *Engine) recomposeSystemLocked() string {
	ro := e.pol.ExtraRootsReadOnly
	return contextload.ComposeSystem(
		contextload.PathJailFacts(e.cfg.Workspace, e.pol.ExtraRoots, ro),
		agent.FramingFacts(e.untrustedNonce),
		e.agents, e.skillsText, e.opt.SystemAppend,
	)
}

// ActivateSkills loads the named skills (case-insensitive folder match) from
// the engine's already-resolved skill directories and merges them into the
// live system prompt for subsequent turns. It is safe to call mid-session:
// it acquires the prompt mutex (no concurrent Prompt), does not mutate
// committed history, and preserves the first-prompt selector and explicit
// CLI/config skills already loaded.
//
// Names not found among discoverable skills are reported in Unknown, not
// errored, so a host can surface them without aborting the good ones.
// Activated returns the folder names actually loaded (first-directory spelling,
// in input order). A name already loaded is a no-op for the prompt but is
// still reported as activated.
func (e *Engine) ActivateSkills(names ...string) (activated, unknown []string) {
	if e == nil {
		return nil, nil
	}
	available := contextload.AvailableSkillNames(e.skillDirs)
	avail := make(map[string]bool, len(available))
	for _, a := range available {
		avail[strings.ToLower(a)] = true
	}
	// Partition requested names into available (activated) vs unknown.
	for _, n := range names {
		k := strings.ToLower(strings.TrimSpace(n))
		if k == "" {
			continue
		}
		if avail[k] {
			activated = append(activated, n)
		} else {
			unknown = append(unknown, n)
		}
	}
	sort.Strings(unknown)
	if len(activated) == 0 {
		return nil, unknown
	}
	// Load the named skills that exist (unknown names are silently skipped by
	// LoadExplicitSkills). First-directory precedence is baked into the loader
	// via the engine's skillDirs.
	loaded := contextload.LoadExplicitSkills(e.skillDirs, names)
	if loaded == "" {
		return nil, unknown
	}
	e.promptMu.Lock()
	defer e.promptMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	// Merge into the live baseline (explicit + prompt-matched + previously
	// activated). mergeSkillText dedupes by label so re-activating an
	// already-loaded skill is a no-op for the prompt.
	e.skillsText = mergeSkillText(e.skillsText, loaded)
	e.sys = e.recomposeSystemLocked()
	e.skillsLoaded = true
	sort.Strings(activated)
	return activated, unknown
}

// AvailableSkills returns the sorted, deduplicated skill folder names the
// engine can load from its configured skill directories (global, user,
// trusted project, and programmatic SkillsDirs). It is the same set the
// /skill listing and ActivateSkills operate on. Missing dirs are skipped.
func (e *Engine) AvailableSkills() []string {
	if e == nil {
		return nil
	}
	return contextload.AvailableSkillNames(e.skillDirs)
}
