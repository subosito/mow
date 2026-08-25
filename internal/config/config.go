// Package config loads mow settings from defaults, optional yaml files, and env.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow/internal/llm"
)

// File is the on-disk / merged configuration.
//
// Core fields stay lean. UI packs and other optional features put their knobs
// under Extensions (e.g. extensions.acp) and decode with Extension().
type File struct {
	Workspace string        `yaml:"workspace"`
	LLM       LLMConfig     `yaml:"llm"`
	Tools     ToolsConfig   `yaml:"tools"`
	Policy    PolicyConfig  `yaml:"policy"`
	Session   SessionConfig `yaml:"session"`
	Skills    SkillsConfig  `yaml:"skills"`
	// OTEL is optional OpenTelemetry export. Empty Endpoint disables export
	// (default). Host/user config only — stripped from project .mow/config.
	OTEL       OTELConfig           `yaml:"otel"`
	Extensions map[string]yaml.Node `yaml:"extensions"`
}

// OTELConfig wires the optional OTLP exporter when Endpoint is non-empty.
// Default (zero value) is off — no exporter process, no network.
type OTELConfig struct {
	// Endpoint is the OTLP collector base URL, e.g. "http://127.0.0.1:4318"
	// or "https://otlp.example.com:4318". Empty = disabled.
	Endpoint string `yaml:"endpoint"`
	// Protocol selects the OTLP transport. "http" (default) or "grpc".
	Protocol string `yaml:"protocol"`
	// ServiceName becomes resource service.name (default "mow").
	ServiceName string `yaml:"service_name"`
	// Headers are extra exporter headers (e.g. authorization).
	Headers map[string]string `yaml:"headers"`
	// endpoint means 1.0 (sample all). Use a small fraction in busy fleets.
}

type LLMConfig struct {
	// Wire is the client protocol:
	//   openai-chat-completions (default) | openai-responses | anthropic-messages
	Wire string `yaml:"wire"`
	// WireExplicit is true when wire was set by yaml, MOW_WIRE, or SetWire —
	// not merely the built-in default. Engine skips catalog wire auto-align
	// when this is true so a pinned openai-chat-completions is not overridden.
	WireExplicit bool   `yaml:"-"`
	BaseURL      string `yaml:"base_url"`
	APIKey       string `yaml:"api_key"`
	APIKeyEnv    string `yaml:"api_key_env"`
	Model        string `yaml:"model"` // provider (or gateway) model id
	// Effort is canonical reasoning intensity: none|low|medium|high.
	// Empty = provider default. Applied by mow (model-id tier rewrite and/or body fields).
	Effort  string            `yaml:"effort"`
	Headers map[string]string `yaml:"headers"`
	Stream  bool              `yaml:"stream"`
	// MaxTokens caps the reply on wires that require the field
	// (anthropic-messages). 0 = use the model's published generation cap from
	// GET /v1/models, falling back to a conservative floor. Raise it when a
	// model can write more than the gateway advertises; a cut reply that was
	// mid tool-call costs a retry and can fail the run.
	MaxTokens int `yaml:"max_tokens"`
	// PromptCache toggles provider prompt caching (anthropic-messages: cache
	// system/tools/history). Nil = enabled (pure win for repeated prefixes);
	// set false for gateways that reject cache_control fields.
	//
	// Accepts a bool or one of short|long|none:
	//   short (= true, default) — provider default TTL (~5 min)
	//   long                    — 1h ephemeral TTL, for interactive sessions
	//                             whose think-time gaps exceed the short window
	//   none  (= false)         — no cache_control at all
	PromptCache *CacheMode `yaml:"prompt_cache"`
	// NativeTools are provider-executed tools declared in the request, given
	// as wire-shaped entries (e.g. [{type: web_search}]). The provider runs
	// them: no local network call, no path jail, no tool.start/tool.end event.
	//
	// Support is per model, not per wire. Declaring one a model cannot run
	// makes it emit a call nothing answers, which leaks into the reply as
	// stray tokens — set this only for models known to support it. Empty
	// (the default) declares nothing.
	NativeTools []map[string]any `yaml:"native_tools"`
	// SystemPrefix is optional text segments prepended before the compiled
	// system prompt (mow harness + AGENTS.md / skills). Each list item is a
	// separate segment (not one joined string). Use for product identity /
	// provider preambles; harness rules still apply. Applies on all wires when
	// the model matches SystemPrefixModels. Not loadable from project
	// .mow/config.
	SystemPrefix []string `yaml:"system_prefix"`
	// SystemPrefixModels limits SystemPrefix to matching model ids (case-
	// insensitive globs: *, ?). Empty = apply for every model when
	// SystemPrefix is set. Example: ["family-*"].
	SystemPrefixModels []string `yaml:"system_prefix_models"`
	// FirstByteTimeoutSec bounds how long a streaming call waits for the
	// first response byte/headers before failing. Long-reasoning models can
	// legitimately spend minutes thinking before the first SSE chunk; the
	// default (300s) matches stream_idle_timeout so "no bytes for X" means
	// the same X before and after the first byte. A full first-byte timeout
	// is a hard, non-retried failure (it does not multiply across attempts).
	// 0 = use the default.
	FirstByteTimeoutSec int `yaml:"first_byte_timeout_sec"`
	// CallTimeoutSec bounds a single non-streaming call (one attempt), not
	// the whole retry sequence. A non-streaming high-effort completion can
	// exceed 120s; raise this for such models. 0 = default (120s).
	CallTimeoutSec int `yaml:"call_timeout_sec"`
	// ContextWindow / InputPrice / OutputPrice override the built-in model
	// catalog (context tokens; USD per 1M input/output tokens) when it is
	// missing or stale for the configured model.
	ContextWindow int     `yaml:"context_window"`
	InputPrice    float64 `yaml:"input_price"`
	OutputPrice   float64 `yaml:"output_price"`

	// Media model ids moved to extensions.media (owned by packs/media).
	// The media tools still share llm.base_url / api_key / headers — only the
	// per-modality model ids are pack config.
}

type ToolsConfig struct {
	// Enable is the built-in tool list. Users normally don't touch it: the
	// Write/Shell booleans below (or --allow-write/--allow-shell) derive it.
	// Kept as the explicit override form for hosts and project overlays
	// (which may only *add* safe tools to it — see dropProjectTools).
	Enable []string `yaml:"enable"`
	// Write enables the write/edit tools by default — the config form of
	// --allow-write. Host/user config only; project overlays cannot set it.
	Write bool `yaml:"write"`
	// Shell enables bash by default — the config form of --allow-shell.
	// Host/user config only; project overlays cannot set it.
	Shell bool `yaml:"shell"`
	// Hashline enables hashline read/edit protocol (config-only; no env).
	Hashline bool `yaml:"hashline"`
}

type PolicyConfig struct {
	// MaxTurns caps LLM round-trips per Prompt. Default 0 = unlimited: a run
	// ends when it is done, stuck (ErrStuck), cancelled, or over an explicitly
	// configured budget — never merely because it was long.
	//
	// Set a positive value to opt in to a ceiling. In YAML, -1 also means
	// unlimited (kept for configs written when 120 was the default; 0 is
	// indistinguishable from "omit" in overlays). CLI: --max-turns N.
	MaxTurns int `yaml:"max_turns"`
	// BashTimeoutSec is the default per-call bash timeout (default 300).
	// A coding agent runs builds and test suites, so this is minutes, not
	// seconds. A single call may ask for longer via the tool's timeout_sec
	// argument, bounded by MaxBashTimeoutSec.
	BashTimeoutSec int `yaml:"bash_timeout_sec"`
	// MaxBashTimeoutSec bounds what one bash call may request via timeout_sec
	// (default 900). Keeps a model from parking on a hung command forever.
	MaxBashTimeoutSec int `yaml:"max_bash_timeout_sec"`
	// Sandbox opts shell execution (the bash tool and proc_start) into an OS
	// jail. "" / "none" (default) keeps today's behavior: --allow-shell runs
	// an unsandboxed `bash -lc` as you. "bwrap" wraps both entry points in
	// bubblewrap (Linux only, requires the `bwrap` binary).
	//
	// It is a filesystem/home jail, not malware containment: network stays on,
	// so `curl | sh` still works. CLI --sandbox overrides this.
	Sandbox      string `yaml:"sandbox"`
	MaxReadBytes int    `yaml:"max_read_bytes"`
	// MaxContextChars soft-limits history before each LLM call (char estimate, not tokens).
	// Default ~100k floor; Engine auto-scales from gateway context_window × CompactRatio
	// when still on the built-in default. Set to -1 to disable compaction. An explicit
	// positive value is an absolute budget (ignores CompactRatio auto-scale).
	MaxContextChars int `yaml:"max_context_chars"`
	// CompactRatio is the fraction of gateway context_window used as soft history
	// budget when auto-scaling (default 0.5 → 1M tokens ≈ 500k tok-eq history,
	// hard-capped at agent.MaxContextCharsHardCap). Clamped to [0.3, 0.95].
	// 0 / omit → default. Ignored when MaxContextChars is an explicit
	// non-default absolute.
	CompactRatio float64 `yaml:"compact_ratio"`
	// MaxToolResultChars caps each tool result stored in history (default 24k).
	// Protects the model from huge read/bash dumps.
	MaxToolResultChars int `yaml:"max_tool_result_chars"`
	// CompactSummary replaces the deterministic compaction stub with a
	// structured summary produced by one extra LLM call (goal, constraints,
	// progress, decisions, next steps, critical context).
	//
	// Off by default: it spends real tokens on a path that currently spends
	// none. It pays when the model would otherwise re-explore after every
	// compaction — long single-task sessions — and may not on short scattered
	// ones. The call is one-shot, so it never writes a prompt-cache entry.
	CompactSummary bool `yaml:"compact_summary"`
	// MaxRunTokens caps InputTokens+OutputTokens for one Prompt. 0 = no limit.
	// The honest spend primitive: works whether or not the gateway publishes
	// prices, unlike MaxRunUSD. MaxTurns bounds round-trips; this bounds cost.
	MaxRunTokens int `yaml:"max_run_tokens"`
	// MaxRunUSD caps projected cost for one Prompt. 0 = no limit. Requires
	// published pricing — mow refuses to start when it is set and the model
	// has no price, rather than pretending to enforce a ceiling it cannot.
	MaxRunUSD float64 `yaml:"max_run_usd"`
	// MaxParallelTools caps concurrent tool Exec in one assistant batch (default 8).
	// Set to 1 for sequential execution.
	MaxParallelTools int `yaml:"max_parallel_tools"`
	// ExtraRoots are additional directory trees FS tools may access (read/write
	// still follow allow-write). Absolute or relative (resolved at load).
	// User/host config and CLI only — stripped from project .mow/config.
	// CLI: repeatable --extra-root PATH.
	ExtraRoots         []string `yaml:"extra_roots"`
	ExtraRootsReadOnly []string `yaml:"extra_roots_read_only"`
}

type SessionConfig struct {
	Dir string `yaml:"dir"`
}

// SkillsConfig lists directories of markdown skill files (*.md).
type SkillsConfig struct {
	Dirs []string `yaml:"dirs"`
	// Selector defaults on. Set false to load every configured skill.
	Selector *bool `yaml:"selector"`
	// Explicit names skill folders to load unconditionally, regardless of the
	// first-prompt selector. Names are matched case-insensitively against
	// folder names in the configured dirs (global, user, and trusted project).
	// Unknown names are silently ignored so a name that exists on one machine
	// but not another does not break config. CLI --skill appends here.
	Explicit []string `yaml:"explicit"`
}

// Load merges configuration layers with increasing precedence:
//
//	defaults → $MOW_HOME/config.yaml → explicit paths → environment
//	→ trusted project .mow/config.yaml (restricted) → re-applied environment
//
// Callers that need a workspace profile overlay should use LoadWithProfile.
// Explicit Options/CLI applied by the engine after Load still win over all of
// the above. For hermetic embedding (no $MOW_HOME), use LoadForEngine(false, …)
// or LoadPaths.
func Load(paths ...string) (*File, error) {
	return LoadWithProfile("", paths...)
}

// LoadPaths merges only defaults → explicit paths → environment.
// It never reads $MOW_HOME/config.yaml, workspace profiles, or trusted project
// config. Used by path-only extension setup (BeforeNew) so a hermetic Engine
// that hands explicit ConfigPaths does not pull implicit global config.
func LoadPaths(paths ...string) (*File, error) {
	return loadConfig(false, "", paths...)
}

// LoadForEngine is the Engine construction entry: when loadUserConfig is true
// it matches LoadWithProfile (CLI/host); when false it is hermetic like
// LoadPaths plus an optional profile name is ignored (profiles are $MOW_HOME).
func LoadForEngine(loadUserConfig bool, profile string, paths ...string) (*File, error) {
	if !loadUserConfig {
		profile = ""
	}
	return loadConfig(loadUserConfig, profile, paths...)
}

// LoadWithProfile is Load plus an optional workspace profile overlay.
//
// Precedence (later wins):
//
//	defaults
//	→ $MOW_HOME/config.yaml          (global user/host config)
//	→ profile config.yaml            (selected $MOW_HOME/workspaces/<name>)
//	→ explicit --config paths        (paths argument; later files win)
//	→ environment
//	→ trusted project .mow/config.yaml (restricted merge)
//	→ environment (re-applied so env still beats project)
//
// Explicit Options/CLI applied by the engine after Load still win.
//
// The profile is operator-owned $MOW_HOME state (more specific than the global
// file, less specific than an explicit --config path). Environment and
// Options always beat files. An empty profile or a profile without config.yaml
// behaves exactly like Load.
//
// Extension sections (extensions.*) are whole-section replaced by the last
// writer — a profile extensions.acp fully replaces the global one rather than
// deep-merging agent maps.
func LoadWithProfile(profile string, paths ...string) (*File, error) {
	return loadConfig(true, profile, paths...)
}

// loadConfig is the shared merge pipeline.
//
// loadUserConfig gates all $MOW_HOME-derived layers: global config.yaml,
// named workspace profile overlay, and trusted project .mow/config.yaml.
// Explicit paths and process env always apply (env/Options still win over
// files). Hermetic embedding passes loadUserConfig=false.
func loadConfig(loadUserConfig bool, profile string, paths ...string) (*File, error) {
	f := defaults()
	if loadUserConfig {
		// Global user config first among files (lowest precedence after defaults).
		_ = mergeFile(f, ConfigPath()) // optional; missing is fine
		// Selected workspace profile: more specific than global, below --config.
		if profile != "" {
			if p, found, perr := LoadProfile(profile); perr != nil {
				return nil, perr
			} else if found && p.HasConfig() {
				if err := mergeFile(f, p.ConfigPath()); err != nil {
					return nil, err
				}
			}
		}
	}
	// Explicit --config / programmatic paths win over global and profile.
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := mergeFile(f, p); err != nil {
			return nil, err
		}
	}
	applyEnv(f)
	if err := f.normalize(); err != nil {
		return nil, err
	}
	// Project-local config only when loading user state and trusted
	// (MOW_TRUST_PROJECT or the out-of-band trust list — see trust.go).
	// Even then the merge is restricted: project files may never set
	// credentials, endpoints, or power tools (mergeProjectFile).
	if loadUserConfig && ProjectConfigAllowed(f.Workspace) {
		_ = mergeProjectFile(f, filepath.Join(f.Workspace, ".mow", "config.yaml"))
		// re-apply env so env still wins
		applyEnv(f)
		_ = f.normalize()
	}
	return f, nil
}

// ProjectConfigAllowed reports whether workspace/.mow/config.yaml may load.
// Trust is stored out-of-band (Home()/trusted, `mow trust`) — never inside
// the workspace, where a cloned repo could grant itself trust.
func ProjectConfigAllowed(workspace string) bool {
	return WorkspaceTrusted(workspace)
}

func defaults() *File {
	return &File{
		Workspace: ".",
		LLM: LLMConfig{
			Wire:      "openai-chat-completions",
			APIKeyEnv: "OPENAI_API_KEY",
		},
		Tools: ToolsConfig{
			// secure default: read-only
			Enable: []string{"read", "glob", "grep"},
		},
		Policy: PolicyConfig{
			// MaxTurns: 0 (unlimited). A turn cap is a poor proxy for cost or
			// progress — 120 turns is trivial for small edits and nowhere near
			// enough for a large refactor — and a default one ends healthy
			// long-running work with nothing wrong. Cost is bounded by
			// max_run_tokens/max_run_usd, spinning by ErrStuck; neither needs
			// a turn ceiling. Set max_turns explicitly to opt back in.
			// 300s: a coding agent runs test suites and builds, not one-liners.
			// A cold `go test ./...` or a nested harness subcommand routinely
			// needs minutes; 60s forced agents into background-process
			// workarounds for ordinary work. Individual calls may request more
			// via the bash timeout_sec arg, up to MaxBashTimeoutSec.
			BashTimeoutSec:     300,
			MaxBashTimeoutSec:  900,
			MaxReadBytes:       512 << 10, // 512 KiB — enough for code files; loop also caps tool results
			MaxContextChars:    100_000,   // default floor; Engine scales up from gateway context_window
			CompactRatio:       0.5,       // of context_window when auto-scaling (1M → ~500k tok-eq, hard-capped)
			MaxToolResultChars: 24_000,    // ~6k tokens max per tool result in history
			MaxParallelTools:   8,         // concurrent tools per assistant batch
		},
		Session: SessionConfig{
			Dir: "",
		},
	}
}

func mergeFile(dst *File, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var overlay File
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	mergeOverlay(dst, &overlay)
	return nil
}

// mergeProjectFile merges a workspace-local config with a reduced privilege
// set: a project file may tune policy knobs, benign tools, and extensions, but
// never credentials, the LLM endpoint/wire, headers, media model routing,
// session location, extra FS roots, spend ceilings (max_tokens, max_run_*,
// prompt_cache, catalog prices), or power/media-write tools — a
// trusted-but-hostile repo must not redirect the API key, flip the wire
// (which changes credential preference), grant itself shell/write, or opt
// into generate_* (those write under media/ without --allow-write).
func mergeProjectFile(dst *File, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var overlay File
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	overlay.Workspace = ""
	overlay.LLM.BaseURL = ""
	overlay.LLM.APIKey = ""
	overlay.LLM.APIKeyEnv = ""
	overlay.LLM.Headers = nil
	// Wire selects protocol and credential env precedence — host/user only.
	overlay.LLM.Wire = ""
	// System prefix is user/host config only (not project-controlled).
	overlay.LLM.SystemPrefix = nil
	overlay.LLM.SystemPrefixModels = nil
	// Media side-lanes share the chat key; project must not point them.
	// The model ids now live in extensions.media, so drop that whole section
	// rather than trusting the pack to re-check: a cloned workspace pointing
	// them at an attacker endpoint would leak the host's API key.
	delete(overlay.Extensions, "media")
	// Native tools make the provider fetch/search on the request's behalf and
	// bill it. That is a capability decision for the host/user, not something
	// a cloned workspace may switch on.
	overlay.LLM.NativeTools = nil
	// Network timeouts are host/user behavior (not project-controlled).
	overlay.LLM.FirstByteTimeoutSec = 0
	overlay.LLM.CallTimeoutSec = 0
	// Reply cap, prompt cache, and catalog metering are host/user spend
	// decisions: a cloned workspace must not raise max_tokens, disable
	// caching, or lie about context_window / prices.
	overlay.LLM.MaxTokens = 0
	overlay.LLM.PromptCache = nil
	overlay.LLM.ContextWindow = 0
	overlay.LLM.InputPrice = 0
	overlay.LLM.OutputPrice = 0
	overlay.Session.Dir = ""
	// OTEL exporter endpoint/headers are host/user only (not project).
	overlay.OTEL = OTELConfig{}
	// Extra FS roots expand the jail — host/CLI only (not project-controlled).
	overlay.Policy.ExtraRoots = nil
	overlay.Policy.ExtraRootsReadOnly = nil
	// Sandbox is a host/user trust decision: a cloned workspace must not be
	// able to turn the jail off (policy.sandbox: none) — or on, which would
	// silently break builds the operator never opted into.
	overlay.Policy.Sandbox = ""
	// Spend ceilings and compact_summary (an extra billed LLM call) are
	// host/user only — a project must not set or raise them.
	overlay.Policy.MaxRunTokens = 0
	overlay.Policy.MaxRunUSD = 0
	overlay.Policy.CompactSummary = false

	// tools.enable: project may only *add* safe tools; never replace the
	// host list (which would drop user-granted power tools or sneak in
	// generate_*). Handle outside mergeOverlay's replace semantics.
	safeEnable := dropProjectTools(overlay.Tools.Enable)
	overlay.Tools.Enable = nil
	// tools.write/shell are the config form of --allow-write/--allow-shell:
	// a power-tool grant is a host/user trust decision, not a project one.
	overlay.Tools.Write = false
	overlay.Tools.Shell = false

	// skills.dirs: project may only add dirs under the workspace (no
	// absolute paths into $HOME/.ssh etc.). Union, do not replace.
	projectSkills := overlay.Skills.Dirs
	overlay.Skills.Dirs = nil
	// skills.explicit is host/user-only: a project must not force-load skills
	// from global/user dirs it does not own (it could name any folder there).
	overlay.Skills.Explicit = nil

	mergeOverlay(dst, &overlay)
	if len(safeEnable) > 0 {
		dst.Tools.Enable = mergeStringList(dst.Tools.Enable, safeEnable)
	}
	if len(projectSkills) > 0 {
		ws := dst.Workspace
		if abs, err := filepath.Abs(ws); err == nil {
			ws = abs
		}
		dst.Skills.Dirs = mergeStringList(dst.Skills.Dirs, skillDirsUnder(ws, projectSkills))
	}
	return nil
}

// dropProjectTools filters tools a project config must never enable:
// write/edit/bash (power) and generate_* (write under media/ without
// --allow-write). understand_* stay allowed (read-only side lane).
func dropProjectTools(enable []string) []string {
	var out []string
	for _, t := range enable {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "write", "edit", "bash",
			"generate_image", "generate_speech", "generate_video":
			continue
		}
		out = append(out, t)
	}
	return out
}

// skillDirsUnder keeps only skill directories that resolve under workspace.
// Prevents a trusted project from injecting SKILL.md from absolute paths
// outside the tree (e.g. /etc, $HOME).
func skillDirsUnder(workspace string, dirs []string) []string {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return nil
	}
	wsAbs, err := filepath.Abs(ws)
	if err != nil {
		return nil
	}
	wsAbs = filepath.Clean(wsAbs)
	if r, err := filepath.EvalSymlinks(wsAbs); err == nil {
		wsAbs = r
	}
	sep := string(filepath.Separator)
	var out []string
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(wsAbs, d)
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
		if abs == wsAbs || strings.HasPrefix(abs, wsAbs+sep) {
			out = append(out, abs)
		}
	}
	return out
}

// mergeStringList appends unique non-empty trimmed strings (order preserved).
func mergeStringList(base, add []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range base {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range add {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func mergeOverlay(dst *File, overlay *File) {
	if strings.TrimSpace(overlay.Workspace) != "" {
		dst.Workspace = overlay.Workspace
	}
	mergeLLM(&dst.LLM, overlay.LLM)
	if len(overlay.Tools.Enable) > 0 {
		dst.Tools.Enable = append([]string(nil), overlay.Tools.Enable...)
	}
	// Hashline is a bool flag — only a true overlay can turn it on (yaml
	// `tools.hashline: false` is indistinguishable from "absent", and the
	// secure default is off anyway).
	if overlay.Tools.Hashline {
		dst.Tools.Hashline = true
	}
	// Write/Shell are the config form of --allow-write/--allow-shell: same
	// bool semantics — only a true overlay turns them on.
	if overlay.Tools.Write {
		dst.Tools.Write = true
	}
	if overlay.Tools.Shell {
		dst.Tools.Shell = true
	}
	// MaxTurns: positive sets the cap; -1 (or any negative) means unlimited (→ 0).
	// Plain 0 in a YAML overlay is indistinguishable from "absent", so use -1
	// or set max_turns: 0 only via a full defaults replace is not supported —
	// prefer max_turns: -1 for unlimited in YAML.
	if overlay.Policy.MaxTurns > 0 {
		dst.Policy.MaxTurns = overlay.Policy.MaxTurns
	} else if overlay.Policy.MaxTurns < 0 {
		dst.Policy.MaxTurns = 0 // unlimited
	}
	if overlay.Policy.BashTimeoutSec > 0 {
		dst.Policy.BashTimeoutSec = overlay.Policy.BashTimeoutSec
	}
	if overlay.Policy.MaxBashTimeoutSec > 0 {
		dst.Policy.MaxBashTimeoutSec = overlay.Policy.MaxBashTimeoutSec
	}
	if overlay.Policy.MaxReadBytes > 0 {
		dst.Policy.MaxReadBytes = overlay.Policy.MaxReadBytes
	}
	if strings.TrimSpace(overlay.Policy.Sandbox) != "" {
		dst.Policy.Sandbox = strings.TrimSpace(overlay.Policy.Sandbox)
	}
	if strings.TrimSpace(overlay.Session.Dir) != "" {
		dst.Session.Dir = overlay.Session.Dir
	}
	// MaxContextChars: positive sets budget; -1 disables (normalize → 0).
	if overlay.Policy.MaxContextChars != 0 {
		dst.Policy.MaxContextChars = overlay.Policy.MaxContextChars
	}
	if overlay.Policy.CompactRatio > 0 {
		dst.Policy.CompactRatio = overlay.Policy.CompactRatio
	}
	if overlay.Policy.MaxToolResultChars > 0 {
		dst.Policy.MaxToolResultChars = overlay.Policy.MaxToolResultChars
	}
	// 0 is omit (no spend ceiling). A later overlay cannot clear an earlier cap.
	if overlay.Policy.MaxRunTokens > 0 {
		dst.Policy.MaxRunTokens = overlay.Policy.MaxRunTokens
	}
	if overlay.Policy.MaxRunUSD > 0 {
		dst.Policy.MaxRunUSD = overlay.Policy.MaxRunUSD
	}
	// Bool flag: yaml false is indistinguishable from absent (default off).
	if overlay.Policy.CompactSummary {
		dst.Policy.CompactSummary = true
	}
	if len(overlay.Policy.ExtraRoots) > 0 {
		dst.Policy.ExtraRoots = mergeStringList(dst.Policy.ExtraRoots, overlay.Policy.ExtraRoots)
	}
	if len(overlay.Policy.ExtraRootsReadOnly) > 0 {
		dst.Policy.ExtraRootsReadOnly = mergeStringList(dst.Policy.ExtraRootsReadOnly, overlay.Policy.ExtraRootsReadOnly)
	}
	if overlay.Policy.MaxParallelTools > 0 {
		dst.Policy.MaxParallelTools = overlay.Policy.MaxParallelTools
	}
	if overlay.LLM.Stream {
		dst.LLM.Stream = true
	}
	if len(overlay.Skills.Dirs) > 0 {
		dst.Skills.Dirs = append([]string(nil), overlay.Skills.Dirs...)
	}
	if overlay.Skills.Selector != nil {
		v := *overlay.Skills.Selector
		dst.Skills.Selector = &v
	}
	if len(overlay.Skills.Explicit) > 0 {
		dst.Skills.Explicit = mergeStringList(dst.Skills.Explicit, overlay.Skills.Explicit)
	}
	mergeOTEL(&dst.OTEL, overlay.OTEL)
	mergeExtensions(dst, overlay.Extensions)
}

// mergeExtensions replaces whole named sections from overlay (last writer wins).
// Sections are not deep-merged — an extension owns its blob.
func mergeOTEL(dst *OTELConfig, o OTELConfig) {
	if s := strings.TrimSpace(o.Endpoint); s != "" {
		dst.Endpoint = s
	}
	if s := strings.TrimSpace(o.Protocol); s != "" {
		dst.Protocol = s
	}
	if s := strings.TrimSpace(o.ServiceName); s != "" {
		dst.ServiceName = s
	}
	if len(o.Headers) > 0 {
		if dst.Headers == nil {
			dst.Headers = map[string]string{}
		}
		for k, v := range o.Headers {
			dst.Headers[k] = v
		}
	}
}

func mergeExtensions(dst *File, overlay map[string]yaml.Node) {
	if len(overlay) == 0 {
		return
	}
	if dst.Extensions == nil {
		dst.Extensions = make(map[string]yaml.Node, len(overlay))
	}
	for name, node := range overlay {
		// Skip empty/null nodes so an empty key does not wipe a prior section.
		if node.Kind == 0 && node.Tag == "" && node.Value == "" {
			continue
		}
		dst.Extensions[name] = node
	}
}

// Extension decodes extensions.<name> into dst. Missing section is a no-op
// (dst keeps its zero/default values). Core does not interpret extension keys.
func (f *File) Extension(name string, dst any) error {
	if f == nil || dst == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" || f.Extensions == nil {
		return nil
	}
	node, ok := f.Extensions[name]
	if !ok {
		return nil
	}
	if node.Kind == 0 && node.Tag == "" && node.Value == "" {
		return nil
	}
	if err := node.Decode(dst); err != nil {
		return fmt.Errorf("extensions.%s: %w", name, err)
	}
	return nil
}

func mergeLLM(dst *LLMConfig, o LLMConfig) {
	if s := strings.TrimSpace(o.Wire); s != "" {
		dst.Wire = s
		dst.WireExplicit = true
	}
	if s := strings.TrimSpace(o.BaseURL); s != "" {
		dst.BaseURL = s
	}
	if s := strings.TrimSpace(o.APIKey); s != "" {
		dst.APIKey = s
	}
	if s := strings.TrimSpace(o.APIKeyEnv); s != "" {
		dst.APIKeyEnv = s
	}
	if s := strings.TrimSpace(o.Model); s != "" {
		dst.Model = s
	}
	if s := strings.TrimSpace(o.Effort); s != "" {
		dst.Effort = s
	}
	if len(o.SystemPrefix) > 0 {
		dst.SystemPrefix = append([]string(nil), o.SystemPrefix...)
	}
	if len(o.SystemPrefixModels) > 0 {
		dst.SystemPrefixModels = append([]string(nil), o.SystemPrefixModels...)
	}
	if len(o.Headers) > 0 {
		if dst.Headers == nil {
			dst.Headers = map[string]string{}
		}
		for k, v := range o.Headers {
			dst.Headers[k] = v
		}
	}
	if len(o.NativeTools) > 0 {
		// Replace, not append: a later layer saying "these tools" must be able
		// to narrow an earlier list, and appending would make an inherited
		// tool impossible to drop.
		dst.NativeTools = o.NativeTools
	}
	if o.FirstByteTimeoutSec != 0 {
		dst.FirstByteTimeoutSec = o.FirstByteTimeoutSec
	}
	if o.CallTimeoutSec != 0 {
		dst.CallTimeoutSec = o.CallTimeoutSec
	}
	// 0 is omit (use catalog / provider default), same as other positive-only knobs.
	if o.MaxTokens > 0 {
		dst.MaxTokens = o.MaxTokens
	}
	if o.PromptCache != nil {
		v := *o.PromptCache
		dst.PromptCache = &v
	}
	if o.ContextWindow > 0 {
		dst.ContextWindow = o.ContextWindow
	}
	if o.InputPrice > 0 {
		dst.InputPrice = o.InputPrice
	}
	if o.OutputPrice > 0 {
		dst.OutputPrice = o.OutputPrice
	}
}

// applyEnv applies only home-adjacent and LLM credential/model envs.
// Power tools, media models, stream, and workspace use yaml or CLI flags.
//
// The wire decides provider-env precedence: on anthropic-messages the
// ANTHROPIC_* variables win over OPENAI_*, so having both keys exported never
// sends the OpenAI key to an Anthropic endpoint. MOW_* always wins.
func applyEnv(f *File) {
	if v := firstEnv("MOW_WIRE"); v != "" {
		f.LLM.Wire = v
		f.LLM.WireExplicit = true
	}
	keyEnvs := []string{"MOW_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"}
	baseEnvs := []string{"MOW_BASE_URL", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL"}
	modelEnvs := []string{"MOW_MODEL", "OPENAI_MODEL", "ANTHROPIC_MODEL"}
	if strings.ToLower(strings.TrimSpace(f.LLM.Wire)) == "anthropic-messages" {
		keyEnvs = []string{"MOW_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"}
		baseEnvs = []string{"MOW_BASE_URL", "ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"}
		modelEnvs = []string{"MOW_MODEL", "ANTHROPIC_MODEL", "OPENAI_MODEL"}
	}
	if v := firstEnv(baseEnvs...); v != "" {
		f.LLM.BaseURL = v
	}
	if v := firstEnv(keyEnvs...); v != "" {
		f.LLM.APIKey = v
	}
	if v := firstEnv(modelEnvs...); v != "" {
		f.LLM.Model = v
	}
	if v := firstEnv("MOW_EFFORT"); v != "" {
		f.LLM.Effort = v
	}
	if v := firstEnv("MOW_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		f.OTEL.Endpoint = v
	}
	if v := firstEnv("MOW_OTEL_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL"); v != "" {
		f.OTEL.Protocol = v
	}
	if v := firstEnv("MOW_OTEL_SERVICE_NAME", "OTEL_SERVICE_NAME"); v != "" {
		f.OTEL.ServiceName = v
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (f *File) normalize() error {
	f.LLM.Wire = strings.ToLower(strings.TrimSpace(f.LLM.Wire))
	if f.LLM.Wire == "" {
		f.LLM.Wire = "openai-chat-completions"
	}
	// Singular alias (common typo / gateway label).
	if f.LLM.Wire == "openai-response" {
		f.LLM.Wire = "openai-responses"
	}
	switch f.LLM.Wire {
	case "openai-chat-completions", "openai-responses", "anthropic-messages":
		// ok
	default:
		return fmt.Errorf("llm.wire must be openai-chat-completions, openai-responses, or anthropic-messages, got %q", f.LLM.Wire)
	}
	if f.LLM.Wire == "anthropic-messages" && (f.LLM.APIKeyEnv == "" || f.LLM.APIKeyEnv == "OPENAI_API_KEY") {
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" || f.LLM.APIKey == "" {
			f.LLM.APIKeyEnv = "ANTHROPIC_API_KEY"
		}
	}
	if s := strings.TrimSpace(f.LLM.Effort); s != "" {
		// Validate early; empty is ok (provider default).
		norm, err := llm.NormalizeEffort(s)
		if err != nil {
			return fmt.Errorf("llm.effort: %w", err)
		}
		f.LLM.Effort = norm
	}
	if f.LLM.FirstByteTimeoutSec < 0 {
		return fmt.Errorf("llm.first_byte_timeout_sec must be >= 0 (0 = default), got %d", f.LLM.FirstByteTimeoutSec)
	}
	if f.LLM.CallTimeoutSec < 0 {
		return fmt.Errorf("llm.call_timeout_sec must be >= 0 (0 = default), got %d", f.LLM.CallTimeoutSec)
	}
	if f.LLM.APIKey == "" && f.LLM.APIKeyEnv != "" {
		f.LLM.APIKey = strings.TrimSpace(os.Getenv(f.LLM.APIKeyEnv))
	}
	// MaxTurns: 0 (default/omitted) is the unlimited sentinel for the agent
	// loop. Negative values (yaml -1, written when 120 was the default) also
	// mean unlimited — normalize them to the same 0.
	if f.Policy.MaxTurns < 0 {
		f.Policy.MaxTurns = 0
	}
	if f.Policy.MaxReadBytes <= 0 {
		f.Policy.MaxReadBytes = 512 << 10
	}
	if f.Policy.BashTimeoutSec <= 0 {
		f.Policy.BashTimeoutSec = 300
	}
	if f.Policy.MaxBashTimeoutSec <= 0 {
		f.Policy.MaxBashTimeoutSec = 900
	}
	// A configured default above the ceiling would be silently clamped per
	// call; raise the ceiling instead so an explicit setting is honoured.
	if f.Policy.MaxBashTimeoutSec < f.Policy.BashTimeoutSec {
		f.Policy.MaxBashTimeoutSec = f.Policy.BashTimeoutSec
	}
	// -1 in yaml disables compaction; normalize to 0 for the agent (off).
	if f.Policy.MaxContextChars < 0 {
		f.Policy.MaxContextChars = 0
	}
	// Compact ratio: default 0.5; clamp to a safe band for headroom.
	if f.Policy.CompactRatio <= 0 {
		f.Policy.CompactRatio = 0.5
	} else if f.Policy.CompactRatio < 0.3 {
		f.Policy.CompactRatio = 0.3
	} else if f.Policy.CompactRatio > 0.95 {
		f.Policy.CompactRatio = 0.95
	}
	if f.Policy.MaxToolResultChars <= 0 {
		f.Policy.MaxToolResultChars = 24_000
	}
	if f.Policy.MaxParallelTools <= 0 {
		f.Policy.MaxParallelTools = 8
	}
	if f.Session.Dir == "" {
		f.Session.Dir = SessionsDir()
	}
	// default base URLs
	if strings.TrimSpace(f.LLM.BaseURL) == "" {
		switch f.LLM.Wire {
		case "anthropic-messages":
			f.LLM.BaseURL = "https://api.anthropic.com"
		default:
			f.LLM.BaseURL = "https://api.openai.com/v1"
		}
	}
	f.LLM.BaseURL = strings.TrimRight(strings.TrimSpace(f.LLM.BaseURL), "/")
	ws, err := filepath.Abs(f.Workspace)
	if err != nil {
		return err
	}
	f.Workspace = ws
	// Extra roots: absolute + cleaned at load so later CWD changes cannot
	// re-point a relative entry. Reject the filesystem root — it would either
	// disable the jail entirely or fail the prefix check (`/` + sep → `//`).
	if len(f.Policy.ExtraRoots) > 0 {
		var roots, roRoots []string
		seenRW, seenRO := map[string]bool{}, map[string]bool{}
		for _, r := range f.Policy.ExtraRoots {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			path, readOnly := SplitExtraRootSpec(r)
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("policy.extra_roots %q: %w", r, err)
			}
			abs = filepath.Clean(abs)
			if abs == string(filepath.Separator) {
				return fmt.Errorf("policy.extra_roots: filesystem root %q is not allowed as a path jail root", r)
			}
			if readOnly {
				if seenRO[abs] {
					continue
				}
				seenRO[abs] = true
				roRoots = append(roRoots, abs)
				continue
			}
			if seenRW[abs] {
				continue
			}
			seenRW[abs] = true
			roots = append(roots, abs)
		}
		f.Policy.ExtraRoots = roots
		if len(roRoots) > 0 {
			f.Policy.ExtraRootsReadOnly = mergeStringList(f.Policy.ExtraRootsReadOnly, roRoots)
		}
	}
	if len(f.Policy.ExtraRootsReadOnly) > 0 {
		var roots []string
		seen := map[string]bool{}
		for _, r := range f.Policy.ExtraRootsReadOnly {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			abs, err := filepath.Abs(r)
			if err != nil {
				return fmt.Errorf("policy.extra_roots_read_only %q: %w", r, err)
			}
			abs = filepath.Clean(abs)
			if abs == string(filepath.Separator) {
				return fmt.Errorf("policy.extra_roots_read_only: filesystem root %q is not allowed as a path jail root", r)
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true
			roots = append(roots, abs)
		}
		f.Policy.ExtraRootsReadOnly = roots
	}
	if s := strings.TrimSpace(f.OTEL.Endpoint); s != "" {
		f.OTEL.Endpoint = s
		proto := strings.ToLower(strings.TrimSpace(f.OTEL.Protocol))
		switch proto {
		case "", "http", "http/protobuf":
			f.OTEL.Protocol = "http"
		case "grpc":
			f.OTEL.Protocol = "grpc"
		default:
			return fmt.Errorf("otel.protocol %q: want http or grpc", f.OTEL.Protocol)
		}
		if f.OTEL.ServiceName == "" {
			f.OTEL.ServiceName = "mow"
		}
	} else {
		f.OTEL = OTELConfig{} // keep disabled clean
	}
	return nil
}

// ResolveAPIKey returns the API key after env expansion.
func (f *File) ResolveAPIKey() string {
	if k := strings.TrimSpace(f.LLM.APIKey); k != "" {
		return k
	}
	if f.LLM.APIKeyEnv != "" {
		return strings.TrimSpace(os.Getenv(f.LLM.APIKeyEnv))
	}
	return ""
}

// ToolEnabled reports whether name is in the enable list.
func (f *File) ToolEnabled(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, t := range f.Tools.Enable {
		if strings.ToLower(strings.TrimSpace(t)) == name {
			return true
		}
	}
	return false
}

// SplitExtraRootSpec parses policy.extra_roots entries.
// "PATH:ro" is read-only; "PATH" / "PATH:rw" are read-write.
func SplitExtraRootSpec(raw string) (path string, readOnly bool) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	switch {
	case strings.HasSuffix(lower, ":ro"):
		return strings.TrimSpace(raw[:len(raw)-3]), true
	case strings.HasSuffix(lower, ":rw"):
		return strings.TrimSpace(raw[:len(raw)-3]), false
	default:
		return raw, false
	}
}
