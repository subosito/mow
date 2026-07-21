// Package config loads mow settings from defaults, optional yaml files, and env.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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
	//   openai-chat-completions (default) | anthropic-messages
	Wire      string            `yaml:"wire"`
	BaseURL   string            `yaml:"base_url"`
	APIKey    string            `yaml:"api_key"`
	APIKeyEnv string            `yaml:"api_key_env"`
	Model     string            `yaml:"model"` // provider (or gateway) model id
	Headers   map[string]string `yaml:"headers"`
	Stream    bool              `yaml:"stream"`

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
	MaxTurns       int `yaml:"max_turns"`
	BashTimeoutSec int `yaml:"bash_timeout_sec"`
	MaxReadBytes   int `yaml:"max_read_bytes"`
	// MaxContextChars soft-limits history before each LLM call (char estimate, not tokens).
	// Default ~100k. Set to -1 to disable compaction.
	MaxContextChars int `yaml:"max_context_chars"`
	// MaxToolResultChars caps each tool result stored in history (default 24k).
	// Protects the model from huge read/bash dumps.
	MaxToolResultChars int `yaml:"max_tool_result_chars"`
	// MaxParallelTools caps concurrent tool Exec in one assistant batch (default 8).
	// Set to 1 for sequential execution.
	MaxParallelTools int `yaml:"max_parallel_tools"`
}

type SessionConfig struct {
	Dir string `yaml:"dir"`
}

// SkillsConfig lists directories of markdown skill files (*.md).
type SkillsConfig struct {
	Dirs []string `yaml:"dirs"`
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
			MaxTurns:           120,
			BashTimeoutSec:     60,
			MaxReadBytes:       512 << 10, // 512 KiB — enough for code files; loop also caps tool results
			MaxContextChars:    100_000,   // soft compaction on by default (~25–30k tokens rough)
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
// set: a project file may tune policy, skills, and extensions, but never
// credentials, the LLM endpoint, headers, session location, or power tools —
// a trusted-but-hostile repo must not be able to redirect the API key or
// grant itself shell.
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
	overlay.Session.Dir = ""
	overlay.Tools.Enable = dropPowerTools(overlay.Tools.Enable)
	mergeOverlay(dst, &overlay)
	return nil
}

// dropPowerTools filters write/edit/bash out of a project enable list.
func dropPowerTools(enable []string) []string {
	var out []string
	for _, t := range enable {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "write", "edit", "bash":
			continue
		}
		out = append(out, t)
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
	if overlay.Policy.MaxToolResultChars > 0 {
		dst.Policy.MaxToolResultChars = overlay.Policy.MaxToolResultChars
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
	switch f.LLM.Wire {
	case "openai-chat-completions", "anthropic-messages":
		// ok
	default:
		return fmt.Errorf("llm.wire must be openai-chat-completions or anthropic-messages, got %q", f.LLM.Wire)
	}
	if f.LLM.Wire == "anthropic-messages" && (f.LLM.APIKeyEnv == "" || f.LLM.APIKeyEnv == "OPENAI_API_KEY") {
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" || f.LLM.APIKey == "" {
			f.LLM.APIKeyEnv = "ANTHROPIC_API_KEY"
		}
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
		f.Policy.BashTimeoutSec = 60
	}
	// -1 in yaml disables compaction; normalize to 0 for the agent (off).
	if f.Policy.MaxContextChars < 0 {
		f.Policy.MaxContextChars = 0
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
