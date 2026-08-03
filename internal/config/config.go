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
	Workspace  string               `yaml:"workspace"`
	LLM        LLMConfig            `yaml:"llm"`
	Tools      ToolsConfig          `yaml:"tools"`
	Policy     PolicyConfig         `yaml:"policy"`
	Session    SessionConfig        `yaml:"session"`
	Skills     SkillsConfig         `yaml:"skills"`
	Extensions map[string]yaml.Node `yaml:"extensions"`
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
	// PromptCache toggles provider prompt caching (anthropic-messages: cache
	// system/tools/history). Nil = enabled (pure win for repeated prefixes);
	// set false for gateways that reject cache_control fields.
	PromptCache *bool `yaml:"prompt_cache"`
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
	// ContextWindow / InputPrice / OutputPrice override the built-in model
	// catalog (context tokens; USD per 1M input/output tokens) when it is
	// missing or stale for the configured model.
	ContextWindow int     `yaml:"context_window"`
	InputPrice    float64 `yaml:"input_price"`
	OutputPrice   float64 `yaml:"output_price"`

	// Generate maps modality → model id for generate_* tools
	// (image → POST /v1/images/generations, speech → /v1/audio/speech, …).
	// Empty means that generate tool is unavailable.
	Generate GenerateConfig `yaml:"generate"`

	// Understand maps modality → model id for side-lane “sense” tools
	// (image / voice / video). Chat model need not be multimodal.
	Understand UnderstandConfig `yaml:"understand"`
}

// GenerateConfig holds model ids for generate_* tools.
type GenerateConfig struct {
	Image  string `yaml:"image"`
	Speech string `yaml:"speech"`
	// SpeechVoice is the default TTS voice when the tool call omits voice.
	// For ElevenLabs this must be a voice_id (not a display name).
	// Empty → tools package built-in default.
	SpeechVoice string `yaml:"speech_voice"`
	Video       string `yaml:"video"`
}

// UnderstandConfig holds model ids for understand_* tools (image / voice / video).
type UnderstandConfig struct {
	Image string `yaml:"image"`
	Voice string `yaml:"voice"`
	Video string `yaml:"video"`
}

type ToolsConfig struct {
	Enable []string `yaml:"enable"`
	// Hashline enables hashline read/edit protocol (config-only; no env).
	Hashline bool `yaml:"hashline"`
}

type PolicyConfig struct {
	// MaxTurns caps LLM round-trips per Prompt (default 120). 0 = unlimited
	// after load. In YAML use max_turns: -1 for unlimited (0 is indistinguishable
	// from "omit" in overlays). CLI: --max-turns 0.
	MaxTurns int `yaml:"max_turns"`
	// BashTimeoutSec is the default per-call bash timeout (default 300).
	// A coding agent runs builds and test suites, so this is minutes, not
	// seconds. A single call may ask for longer via the tool's timeout_sec
	// argument, bounded by MaxBashTimeoutSec.
	BashTimeoutSec int `yaml:"bash_timeout_sec"`
	// MaxBashTimeoutSec bounds what one bash call may request via timeout_sec
	// (default 900). Keeps a model from parking on a hung command forever.
	MaxBashTimeoutSec int `yaml:"max_bash_timeout_sec"`
	MaxReadBytes      int `yaml:"max_read_bytes"`
	// MaxContextChars soft-limits history before each LLM call (char estimate, not tokens).
	// Default ~100k floor; Engine auto-scales from gateway context_window × CompactRatio
	// when still on the built-in default. Set to -1 to disable compaction. An explicit
	// positive value is an absolute budget (ignores CompactRatio auto-scale).
	MaxContextChars int `yaml:"max_context_chars"`
	// CompactRatio is the fraction of gateway context_window used as soft history
	// budget when auto-scaling (default 0.8 → 1M tokens ≈ 800k tok-eq history).
	// Clamped to [0.3, 0.95]. 0 / omit → default. Ignored when MaxContextChars is
	// an explicit non-default absolute.
	CompactRatio float64 `yaml:"compact_ratio"`
	// MaxToolResultChars caps each tool result stored in history (default 24k).
	// Protects the model from huge read/bash dumps.
	MaxToolResultChars int `yaml:"max_tool_result_chars"`
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
}

// Load merges defaults, optional config paths, then environment.
func Load(paths ...string) (*File, error) {
	f := defaults()
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := mergeFile(f, p); err != nil {
			return nil, err
		}
	}
	// default user config ($MOW_HOME/config.yaml, default ~/.mow/config.yaml)
	_ = mergeFile(f, ConfigPath()) // optional
	applyEnv(f)
	if err := f.normalize(); err != nil {
		return nil, err
	}
	// Project-local config only when trusted (MOW_TRUST_PROJECT or the
	// out-of-band trust list — see trust.go). Even then the merge is
	// restricted: project files may never set credentials, endpoints, or
	// power tools (mergeProjectFile).
	if ProjectConfigAllowed(f.Workspace) {
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
			MaxTurns: 120,
			// 300s: a coding agent runs test suites and builds, not one-liners.
			// A cold `go test ./...` or a nested harness subcommand routinely
			// needs minutes; 60s forced agents into background-process
			// workarounds for ordinary work. Individual calls may request more
			// via the bash timeout_sec arg, up to MaxBashTimeoutSec.
			BashTimeoutSec:     300,
			MaxBashTimeoutSec:  900,
			MaxReadBytes:       512 << 10, // 512 KiB — enough for code files; loop also caps tool results
			MaxContextChars:    100_000,   // default floor; Engine scales up from gateway context_window
			CompactRatio:       0.8,       // of context_window when auto-scaling (1M → ~800k tok-eq)
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
// session location, extra FS roots, or power/media-write tools — a
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
	overlay.LLM.Generate = GenerateConfig{}
	overlay.LLM.Understand = UnderstandConfig{}
	overlay.Session.Dir = ""
	// Extra FS roots expand the jail — host/CLI only (not project-controlled).
	overlay.Policy.ExtraRoots = nil
	overlay.Policy.ExtraRootsReadOnly = nil

	// tools.enable: project may only *add* safe tools; never replace the
	// host list (which would drop user-granted power tools or sneak in
	// generate_*). Handle outside mergeOverlay's replace semantics.
	safeEnable := dropProjectTools(overlay.Tools.Enable)
	overlay.Tools.Enable = nil

	// skills.dirs: project may only add dirs under the workspace (no
	// absolute paths into $HOME/.ssh etc.). Union, do not replace.
	projectSkills := overlay.Skills.Dirs
	overlay.Skills.Dirs = nil

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

// dropPowerTools is the historical name used in tests/docs; same filter as
// dropProjectTools (power + media-write).
func dropPowerTools(enable []string) []string {
	return dropProjectTools(enable)
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
	mergeExtensions(dst, overlay.Extensions)
}

// mergeExtensions replaces whole named sections from overlay (last writer wins).
// Sections are not deep-merged — an extension owns its blob.
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
	if s := strings.TrimSpace(o.Generate.Image); s != "" {
		dst.Generate.Image = s
	}
	if s := strings.TrimSpace(o.Generate.Speech); s != "" {
		dst.Generate.Speech = s
	}
	if s := strings.TrimSpace(o.Generate.Video); s != "" {
		dst.Generate.Video = s
	}
	if s := strings.TrimSpace(o.Understand.Image); s != "" {
		dst.Understand.Image = s
	}
	if s := strings.TrimSpace(o.Understand.Voice); s != "" {
		dst.Understand.Voice = s
	}
	if s := strings.TrimSpace(o.Understand.Video); s != "" {
		dst.Understand.Video = s
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
	if f.LLM.APIKey == "" && f.LLM.APIKeyEnv != "" {
		f.LLM.APIKey = strings.TrimSpace(os.Getenv(f.LLM.APIKeyEnv))
	}
	// MaxTurns: defaults() sets 120. Negative values (yaml -1) mean unlimited → 0.
	// Do not rewrite 0 to 120 — 0 is the unlimited sentinel for the agent loop.
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
	// Compact ratio: default 0.8; clamp to a safe band for headroom.
	if f.Policy.CompactRatio <= 0 {
		f.Policy.CompactRatio = 0.8
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
			path, readOnly := splitExtraRootSpec(r)
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

// splitExtraRootSpec parses policy.extra_roots entries.
// "PATH:ro" is read-only; "PATH" / "PATH:rw" are read-write.
func splitExtraRootSpec(raw string) (path string, readOnly bool) {
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
