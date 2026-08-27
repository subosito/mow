package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/contextload"
	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/policy"
	"github.com/subosito/mow/internal/sandbox"
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
	pluginRoots    []string
	skillSelect    bool
	skillsLoaded   bool
	skillsText     string // cumulative loaded skill markdown (explicit + prompt-matched + activated)
	explicitSkills []string
	opt            Options
	sess           *session.Store
	sid            string
	prior          []llm.Message
	transcript     []Message // user/assistant only (session resume)
	// lastProviderTokens is the last provider-reported input token count.
	// 0 until the first call. Never overwritten by a char-budget estimate.
	lastProviderTokens int
	// lastCtxEstimate is a post-compact token guess. ContextTokens prefers
	// lastProviderTokens when the provider has reported usage.
	lastCtxEstimate int
	// runEffort is a per-Prompt wire override (auto-downshift for short
	// mechanical prompts). Empty means use client/cfg effort. Effort() never
	// returns this — hosts show the session/user setting while the request
	// may send a cheaper tier.
	runEffort string
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
	// onRetryFn is the current run's retry reporter (Prompt-scoped, like
	// onToken): the built-in client and cooperative providers feed it.
	onRetryFn   func(RetryInfo)
	onEvents    []eventSub // fan-out; AddOnEvent / SetOnEvent
	nextEventID uint64

	// activeMu guards modelActiveFn, the CURRENT LLM call's first-byte
	// signal, registered by the loop per call (SetModelActive) and invoked
	// on the first streamed delta or upstream frame.
	activeMu      sync.Mutex
	modelActiveFn func()

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

// NewHarness constructs an Engine the way mow run / mow acp / host UIs do:
// $MOW_HOME and project config are loaded. Embedders that want a hermetic
// engine (Options + env only) should call New instead.
func NewHarness(opt Options) (*Engine, error) {
	opt.LoadUserConfig = true
	return New(opt)
}

// New builds a hermetic Engine from Options (config, tools, optional session).
//
// LoadUserConfig is false unless the caller sets it: New does not read
// or use $MOW_HOME user state (global config, workspace profiles, trust,
// global AGENTS/skills, user sessions, extension home fallbacks). Host
// programs should call NewHarness instead (or set LoadUserConfig). Explicit
// ConfigPaths always load when provided (defaults → paths → env → Options),
// without implicit global config when LoadUserConfig is false.
func New(opt Options) (*Engine, error) {
	// --workspace / Options.Workspace is hybrid only with LoadUserConfig:
	// first looked up as a workspace profile under $MOW_HOME/workspaces/<name>
	// (workspace.yaml root + extra_roots, config.yaml overlay, AGENTS.md,
	// skills/); otherwise a plain directory path. Hermetic mode skips profile
	// lookup entirely — Workspace must be an existing directory when set.
	//
	// Resolve the profile before pre-New extension setup so its config.yaml
	// participates in config-driven registration (e.g. acp_delegate peers).
	// Path-only BeforeNew hooks receive the profile via OverlayConfigPaths
	// (the overlay path is passed even when config.yaml is absent, so
	// plugin MCP/hooks still discover $MOW_HOME/workspaces/<name>/plugins/).
	// when LoadUserConfig, the global config path is prepended so DecodeSection
	// / RegisterFromConfig see host state without each pack re-opening Home.
	// Main load precedence (LoadUserConfig true):
	// defaults < global < profile < explicit --config < env < Options.
	// Hermetic: defaults < explicit ConfigPaths < env < Options.
	var activeProfile *config.Profile
	if opt.LoadUserConfig {
		if w := strings.TrimSpace(opt.Workspace); w != "" {
			profile, found, perr := config.LoadProfile(w)
			if perr != nil {
				return nil, perr
			}
			if found {
				activeProfile = &profile
			}
		}
	}
	beforePaths := append([]string(nil), opt.ConfigPaths...)
	if activeProfile != nil {
		beforePaths = activeProfile.OverlayConfigPaths(beforePaths)
	}
	if opt.LoadUserConfig {
		// Host mode: ensure $MOW_HOME/config.yaml is visible to BeforeNew hooks
		// (extcfg no longer falls back to Home on its own).
		beforePaths = prependConfigPath(beforePaths, config.ConfigPath())
	}
	extGen := 0
	if !opt.SkipExtensionSetup {
		if err := ext.BeforeNew(beforePaths...); err != nil {
			return nil, fmt.Errorf("extension init: %w", err)
		}
		extGen = ext.BeforeNewGeneration()
	}
	profileName := ""
	if activeProfile != nil {
		profileName = activeProfile.Name
	}
	cfg, err := config.LoadForEngine(opt.LoadUserConfig, profileName, opt.ConfigPaths...)
	if err != nil {
		return nil, err
	}
	// Explicit Options overrides (do not mutate process env).
	// A matched profile wins for workspace roots; its extra_roots are
	// prepended to policy.extra_roots — explicit --extra-root flags still
	// append on top.
	if w := strings.TrimSpace(opt.Workspace); w != "" {
		if activeProfile != nil {
			if activeProfile.WorkspaceSet.Root != "" {
				ws, roots, rerr := activeProfile.WorkspaceSet.ResolveWorkspaceSet()
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
			}
		} else if !opt.LoadUserConfig {
			// Hermetic: directory path only — no profile names from $MOW_HOME.
			if fi, serr := os.Stat(w); serr != nil || !fi.IsDir() {
				return nil, fmt.Errorf("workspace %q is not an existing directory (workspace profiles require Options.LoadUserConfig)", w)
			}
			cfg.Workspace = w
		} else if !config.IsWorkspaceProfileName(w) {
			if fi, serr := os.Stat(w); serr != nil || !fi.IsDir() {
				return nil, fmt.Errorf("workspace %q is not a valid profile name (use a single directory name under $MOW_HOME/workspaces, or an existing directory path)", w)
			}
			cfg.Workspace = w
		} else if fi, serr := os.Stat(w); serr == nil && fi.IsDir() {
			cfg.Workspace = w
		} else {
			names, nerr := config.ListProfiles()
			if nerr != nil {
				return nil, nerr
			}
			if len(names) > 0 {
				return nil, fmt.Errorf("workspace profile %q not found and not a directory (defined profiles: %s)", w, strings.Join(names, ", "))
			}
			return nil, fmt.Errorf("workspace profile %q not found and not a directory", w)
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
		// Shape check only: --effort / handshake effort arrives before the
		// catalog, so catalog-only tiers (max, xhigh) stay legal here and are
		// reconciled by SyncEffortToModel once GET /models lands.
		norm, nerr := llm.NormalizeEffortConfigured(e)
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
	// tools.write/shell are the config form of --allow-write/--allow-shell:
	// derive the enable list from them the same way flags do below, so the
	// user never maintains the list by hand. Flags still layer on top.
	if cfg.Tools.Write {
		cfg.Tools.Enable = appendUnique(cfg.Tools.Enable, "write", "edit")
	}
	if cfg.Tools.Shell {
		cfg.Tools.Enable = appendUnique(cfg.Tools.Enable, "bash")
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

	// Sandbox: CLI --sandbox wins over policy.sandbox in config. Parsed here so
	// an invalid value fails at New rather than at the first bash call.
	sbSrc := cfg.Policy.Sandbox
	if s := strings.TrimSpace(opt.Sandbox); s != "" {
		sbSrc = s
	}
	sbMode, err := sandbox.ParseMode(sbSrc)
	if err != nil {
		return nil, fmt.Errorf("policy.sandbox: %w", err)
	}

	pol := &policy.Policy{
		Workspace: cfg.Workspace, ExtraRoots: append([]string(nil), cfg.Policy.ExtraRoots...),
		ExtraRootsReadOnly: append([]string(nil), cfg.Policy.ExtraRootsReadOnly...),
		WritableRoots:      append([]string(nil), opt.WritableRoots...),
		ReadOnly:           opt.ReadOnly,
		AllowWrite:         cfg.ToolEnabled("write") || cfg.ToolEnabled("edit"),
		AllowShell:         cfg.ToolEnabled("bash"),
		Sandbox:            sbMode,
		MaxReadBytes:       cfg.Policy.MaxReadBytes,
		BashTimeoutSec:     cfg.Policy.BashTimeoutSec,
		MaxBashTimeoutSec:  cfg.Policy.MaxBashTimeoutSec,
		Hashline:           cfg.Tools.Hashline,
	}

	enabled := cfg.Tools.Enable
	toolList := tools.Registry(pol, enabled)
	readOnlyExt := map[string]bool{}
	// Hermetic engines skip config-sourced process-global tools left by a
	// prior host New; static init tools and tools from this BeforeNew still
	// merge (ToolsForEngine). LoadUserConfig includes every registered tool.
	// SkipExtensionSetup omits all ext tools (review/sec isolation).
	if !opt.SkipExtensionSetup {
		for _, t := range ext.ToolsForEngine(opt.LoadUserConfig) {
			toolList = append(toolList, adaptTool(t))
			// Ext tools may declare themselves side-effect free; only those run
			// in read-only prompts (see isReadOnlyTool).
			if ro, ok := t.(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
				readOnlyExt[strings.ToLower(strings.TrimSpace(t.Name()))] = true
			}
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
	// Hermetic mode skips global $MOW_HOME/AGENTS.md; workspace-chain files remain.
	var agents string
	if opt.LoadUserConfig {
		agents, _ = contextload.Load(cfg.Workspace)
	} else {
		agents, _ = contextload.LoadHermetic(cfg.Workspace)
	}
	if activeProfile != nil && activeProfile.HasAgents() {
		if extra, aerr := os.ReadFile(activeProfile.AgentsPath()); aerr == nil {
			if text := strings.TrimSpace(string(extra)); text != "" {
				agents = strings.TrimSpace(text + "\n\n" + agents)
			}
		}
	}
	skillDirs := append([]string(nil), cfg.Skills.Dirs...)
	var pluginRoots []string
	var workspacePluginRoot, projectPluginRoot string
	// Profile-local skills are the most specific operator-authored skills, so
	// search them before global/config/project sources.
	if activeProfile != nil && activeProfile.HasSkills() {
		skillDirs = append([]string{activeProfile.SkillsDir()}, skillDirs...)
	}
	if opt.LoadUserConfig {
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
		// Global $MOW_HOME/skills — host only.
		skillDirs = append([]string{config.SkillsDir()}, skillDirs...)
		pluginRoots = []string{config.PluginsDir()}
		if activeProfile != nil {
			workspacePluginRoot = activeProfile.PluginsDir()
			if activeProfile.HasPlugins() {
				pluginRoots = append(pluginRoots, workspacePluginRoot)
			}
		}
		if contextload.ProjectTrusted(cfg.Workspace) {
			projectPluginRoot = filepath.Join(cfg.Workspace, ".mow", "plugins")
			pluginRoots = append(pluginRoots, projectPluginRoot)
		}
		skillDirs = append(skillDirs, contextload.PluginSkillDirs(pluginRoots)...)
	}
	// Programmatic dirs (tests/hosts) are searched last, after global/config
	// dirs, so config and the global $MOW_HOME/skills still take precedence
	// when LoadUserConfig is on. Hermetic embedders use SkillsDirs alone.
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
	explicitSkills = mergeSkillNames(explicitSkills, contextload.PluginDefaultSkillNames(pluginRoots))
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
	pluginFacts := contextload.PluginInstallFacts(config.PluginsDir(), workspacePluginRoot, projectPluginRoot)
	// Packs contribute their own system-prompt segments (guidance for their
	// tools) via ext.RegisterSystemSegment, so advice travels with the
	// capability: no pack linked, no segment.
	sysParts := append([]string{jailFacts, pluginFacts, agents, skills}, ext.SystemSegments(opt.ConfigPaths...)...)
	sys := contextload.ComposeSystem(append(sysParts, opt.SystemAppend)...)

	loopHooks, life := mergeHooks(opt)

	// Hermetic embedding defaults to no session persistence: session.dir
	// otherwise falls under $MOW_HOME/sessions. There is no separate safe
	// session-dir Options field yet; hosts that want sessions set
	// LoadUserConfig. Explicit NoSession remains respected either way.
	noSess := opt.NoSession || !opt.LoadUserConfig

	e := &Engine{
		cfg:            cfg,
		pol:            pol,
		sys:            sys,
		agents:         agents,
		skillDirs:      append([]string(nil), skillDirs...),
		pluginRoots:    append([]string(nil), pluginRoots...),
		skillSelect:    selectSkills,
		skillsLoaded:   !selectSkills,
		skillsText:     skills,
		explicitSkills: explicitSkills,
		opt:            opt,
		noSess:         noSess,
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
	if extGen > 0 {
		ext.NoteEngineGeneration(extGen)
		gen := extGen
		e.RegisterCleanup(func() { ext.ReleaseEngineGeneration(gen) })
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
			hooks := ChatHooks{OnToken: e.onToken, OnReasoning: e.onReasoning, OnRetry: e.onRetryFn}
			e.onTokenMu.Unlock()
			hooks.OnActivity = e.signalModelActive
			out, err := prov.Chat(ctx, toPublicMessages(messages), toPublicToolSpecs(tools), hooks)
			if err != nil {
				return llm.Message{}, err
			}
			if out.Usage.InputTokens > 0 {
				e.mu.Lock()
				e.lastProviderTokens = out.Usage.InputTokens
				e.lastCtxEstimate = 0
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
				e.lastProviderTokens = msg.Usage.InputTokens
				e.lastCtxEstimate = 0
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
		if opt.DeferLLM {
			break
		}
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
			hooks := llm.StreamHooks{OnContent: e.onToken, OnReasoning: e.onReasoning, OnActivity: e.signalModelActive}
			e.onTokenMu.Unlock()
			// Snapshot all mutable request/catalog state under e.mu. ListModels
			// publishes refreshed catalogs under the same lock, so a running call
			// never shares maps or slices with model switching/catalog refresh.
			e.mu.Lock()
			c := cloneRequestClient(e.client, e.runEffort)
			e.mu.Unlock()
			c.OnRetry = e.llmRetryHook()
			msg, err := c.ChatWithStream(ctx, messages, tools, hooks)
			if err == nil && msg.Usage.InputTokens > 0 {
				e.mu.Lock()
				e.lastProviderTokens = msg.Usage.InputTokens
				e.lastCtxEstimate = 0
				e.mu.Unlock()
			}
			return msg, err
		}
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
	e.tools = final
	// Enable names that never landed in the registry (wrong binary, or a
	// pack that did not RegisterTool) stay a no-op — warn so the operator
	// is not stuck with a silent missing tool.
	if miss := unknownRegisteredEnable(enabled, toolList); len(miss) > 0 {
		msg := "mow: " + tools.FormatUnregisteredEnable(miss, mediaPackLinked())
		if opt.Logger != nil {
			opt.Logger.Warn(msg)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
	}
	// Per-engine nonce for untrusted-output framing (bash/MCP/delegate).
	nb := make([]byte, 8)
	if _, err := rand.Read(nb); err == nil {
		e.untrustedNonce = hex.EncodeToString(nb)
	}

	if !noSess {
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
					if runtime.Wire != "" {
						e.cfg.LLM.Wire = runtime.Wire
					}
				}
			}
			if runtime.Effort != "" && !opt.ExplicitEffort && !opt.ExplicitModel {
				// Restore the effort last used by this session with the same
				// precedence as the model: only --effort wins over it. Skipped
				// when the model is explicit — the caller pinned a model whose
				// catalog efforts may not include the session's stored tier,
				// and SetModel already synced effort for that model.
				e.mu.Lock()
				norm := ""
				if e.client != nil {
					// Catalog is loaded: accept only tiers the restored
					// model advertises.
					if n, nerr := llm.NormalizeEffortFor(runtime.Effort, e.client.EffortsForModel(e.client.Model)); nerr == nil {
						norm = n
					}
				} else {
					// DeferLLM (mow acp): no catalog yet, and the static
					// none|low|medium|high list would reject catalog-only
					// tiers (max, xhigh) — exactly what a session persists.
					// Take the recorded tier as-is; ensureLLM runs
					// SyncEffortToModel once the catalog loads, keeping it
					// when the model allows it and landing on default_effort
					// otherwise.
					switch n := strings.ToLower(strings.TrimSpace(runtime.Effort)); n {
					case "", "default", "auto":
					default:
						norm = n
					}
				}
				if norm != "" {
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
			turns, err := store.LoadTranscriptEvents()
			if err != nil {
				return nil, fmt.Errorf("session transcript: %w", err)
			}
			e.transcript = make([]Message, 0, len(turns))
			for _, turn := range turns {
				e.transcript = append(e.transcript, Message{Role: turn.Role, Content: turn.Content, Timestamp: turn.TS})
			}
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

	// Fail closed on an unenforceable spend ceiling. policy.max_run_usd on a
	// model with no published price cannot fire; starting anyway would hand
	// the operator a limit that silently never triggers, which is worse than
	// having none. Refuse at construction, where the message is actionable.
	if _, err := e.budgetGate(); err != nil {
		return nil, err
	}

	return e, nil
}

// AddTool adds or replaces an engine-scoped extension tool. It is intended for
// profile-aware extensions that must not leak process-global registrations
// into other Engines. Builtin names remain reserved.
func (e *Engine) AddTool(t Tool) error {
	if e == nil || t == nil {
		return fmt.Errorf("mow: nil engine/tool")
	}
	name := strings.ToLower(strings.TrimSpace(t.Name()))
	if name == "" {
		return fmt.Errorf("mow: tool name is required")
	}
	if isBuiltin(name) {
		return fmt.Errorf("mow: cannot replace builtin tool %q", name)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	adapted := adaptTool(t)
	for idx, existing := range e.tools {
		if strings.EqualFold(existing.Name(), name) {
			e.tools[idx] = adapted
			return nil
		}
	}
	e.tools = append(e.tools, adapted)
	if ro, ok := any(t).(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
		e.readOnlyExt[name] = true
	}
	return nil
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

// WritableRoots returns explicit writable-root exceptions for a read-only workspace.
func (e *Engine) WritableRoots() []string {
	if e == nil || e.pol == nil {
		return nil
	}
	return append([]string(nil), e.pol.WritableRoots...)
}

// ReadOnlyWorkspace reports whether the primary workspace is read-only.
func (e *Engine) ReadOnlyWorkspace() bool {
	return e != nil && e.pol != nil && e.pol.ReadOnly
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

// BeginSession starts a fresh JSONL session on this Engine. The previous file
// stays on disk (Sessions still lists it). Refuses while a prompt is in flight.
//
// NoSession engines only clear in-memory history and return "". Hosts that need
// a different on-disk id should construct a new Engine with Options.SessionID.
func (e *Engine) BeginSession() (string, error) {
	if e == nil {
		return "", fmt.Errorf("mow: nil engine")
	}
	e.promptMu.Lock()
	defer e.promptMu.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return "", fmt.Errorf("mow: engine closed")
	}
	e.mu.Unlock()
	if e.Status().Busy {
		return "", fmt.Errorf("mow: prompt in flight")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.prior = nil
	e.transcript = nil
	e.steer = nil
	e.lastProviderTokens = 0
	e.lastCtxEstimate = 0
	if e.noSess {
		e.sid = ""
		e.sess = nil
		return "", nil
	}
	sessDir := ""
	if e.sess != nil {
		sessDir = e.sess.Dir
	} else if e.cfg != nil {
		sessDir = filepath.Join(e.cfg.Session.Dir, projectHash(e.cfg.Workspace))
	}
	if sessDir == "" {
		return "", fmt.Errorf("mow: session dir required")
	}
	sid := session.NewID()
	if sid == e.sid {
		sid = fmt.Sprintf("%s-%d", sid, time.Now().UnixNano()%1_000_000)
	}
	if err := session.ValidateID(sid); err != nil {
		return "", err
	}
	e.sid = sid
	e.sess = &session.Store{Dir: sessDir, ID: sid}
	return sid, nil
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

// ShellSandbox returns the sandbox backend that shell tools must wrap their
// commands in (identity when --sandbox is unset). Every shell entry point —
// the bash tool and proc_start — has to use it, or the unwrapped one becomes
// the escape hatch. An unavailable backend is an error, never a silent
// fallback to a raw shell.
func (e *Engine) ShellSandbox() (sandbox.Backend, error) {
	if e == nil || e.pol == nil {
		return sandbox.None{}, nil
	}
	return e.pol.SandboxBackend()
}

// SandboxMode reports the configured shell sandbox ("none" or "bwrap").
func (e *Engine) SandboxMode() string {
	if e == nil || e.pol == nil || !e.pol.SandboxEnabled() {
		return string(sandbox.ModeNone)
	}
	return string(e.pol.Sandbox)
}

// ensureLLM attaches the built-in LLM client when New ran with DeferLLM.
// Injected Chat/Provider engines are already ready. Safe to call more than once.
func (e *Engine) ensureLLM() error {
	if e == nil {
		return fmt.Errorf("mow: nil engine")
	}
	if e.chat != nil {
		return nil
	}
	if e.opt.Provider != nil || e.opt.Chat != nil {
		return fmt.Errorf("mow: chat backend missing")
	}
	cfg := e.cfg
	if cfg == nil {
		return fmt.Errorf("mow: engine has no config")
	}
	key := cfg.ResolveAPIKey()
	if key == "" {
		return fmt.Errorf("api key required (OPENAI_API_KEY / MOW_API_KEY / ANTHROPIC_API_KEY, or llm.api_key in the config file under MOW_HOME)")
	}
	if strings.TrimSpace(cfg.LLM.Model) == "" && !e.opt.Continue && strings.TrimSpace(e.opt.SessionID) == "" {
		return fmt.Errorf("model required (OPENAI_MODEL / MOW_MODEL / ANTHROPIC_MODEL or llm.model)")
	}
	headers := withActorHeaders(cfg.LLM.Headers, "mow")
	client := &llm.Client{
		Wire:               cfg.LLM.Wire,
		BaseURL:            cfg.LLM.BaseURL,
		APIKey:             key,
		Model:              cfg.LLM.Model,
		Effort:             cfg.LLM.Effort,
		EffortPinned:       strings.TrimSpace(cfg.LLM.Effort) != "",
		HTTP:               e.opt.HTTPClient,
		ExtraHeaders:       headers,
		Stream:             cfg.LLM.Stream || e.opt.Stream,
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
	if client.ExtraHeaders[llm.HeaderComponent] == "" {
		client.ExtraHeaders[llm.HeaderComponent] = "turn.chat"
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = client.ListModels(ctx)
		cancel()
		client.SyncEffortToModel(client.Model)
		cfg.LLM.Effort = client.Effort
		e.applyPreferredWireFromCatalog()
	}
	e.chat = func(ctx context.Context, messages []llm.Message, specs []llm.ToolSpec) (llm.Message, error) {
		e.onTokenMu.Lock()
		hooks := llm.StreamHooks{OnContent: e.onToken, OnReasoning: e.onReasoning, OnActivity: e.signalModelActive}
		e.onTokenMu.Unlock()
		e.mu.Lock()
		c := cloneRequestClient(e.client, e.runEffort)
		e.mu.Unlock()
		c.OnRetry = e.llmRetryHook()
		msg, err := c.ChatWithStream(ctx, messages, specs, hooks)
		if err == nil && msg.Usage.InputTokens > 0 {
			e.mu.Lock()
			e.lastProviderTokens = msg.Usage.InputTokens
			e.lastCtxEstimate = 0
			e.mu.Unlock()
		}
		return msg, err
	}
	return nil
}

// cacheTTL maps the configured prompt-cache mode to a wire ttl. Unset means
// the provider default.
func cacheTTL(m *config.CacheMode) string {
	if m == nil {
		return ""
	}
	return m.TTL()
}

// prependConfigPath puts the global user config path first in the BeforeNew
// path list when the host opted into LoadUserConfig, without duplicating it.
