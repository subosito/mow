// Package mow is the public Engine API. The implementation lives in
// internal/engine/; this file re-exports the public surface as type aliases
// and thin function wrappers so callers use mow.Engine, mow.Run, etc.
package mow

import (
	"context"

	"github.com/subosito/mow/internal/engine"
)

// --- Types (type aliases carry methods) ---

type Tool = engine.Tool
type Message = engine.Message
type ToolCall = engine.ToolCall
type FunctionCall = engine.FunctionCall
type ToolSpec = engine.ToolSpec
type ToolSpecFunction = engine.ToolSpecFunction
type ChatFunc = engine.ChatFunc
type ChatHooks = engine.ChatHooks
type Provider = engine.Provider
type ModelLister = engine.ModelLister
type ModelSwitcher = engine.ModelSwitcher

// RetryInfo / RetryKind describe one scheduled model-call retry, reported
// via ChatHooks.OnRetry and surfaced as EventModelRetry. They never carry
// URLs, headers, or credentials.
type RetryInfo = engine.RetryInfo
type RetryKind = engine.RetryKind
type Engine = engine.Engine
type Options = engine.Options
type RunResult = engine.RunResult
type Usage = engine.Usage
type PromptOpts = engine.PromptOpts
type ModelInfo = engine.ModelInfo
type ModelLimits = engine.ModelLimits
type PromptCostEstimate = engine.PromptCostEstimate
type CompactReport = engine.CompactReport
type SessionInfo = engine.SessionInfo
type SkillInfo = engine.SkillInfo
type PluginInfo = engine.PluginInfo

type EventType = engine.EventType
type CompactLayer = engine.CompactLayer
type DiagnosticSeverity = engine.DiagnosticSeverity
type Diagnostic = engine.Diagnostic
type GoalNode = engine.GoalNode
type GoalEvent = engine.GoalEvent
type Event = engine.Event
type EventFunc = engine.EventFunc
type Status = engine.Status

type PreToolEvent = engine.PreToolEvent
type PreToolDecision = engine.PreToolDecision
type PreToolFunc = engine.PreToolFunc
type PostToolEvent = engine.PostToolEvent
type PostToolDecision = engine.PostToolDecision
type PostToolFunc = engine.PostToolFunc
type UserPromptEvent = engine.UserPromptEvent
type UserPromptDecision = engine.UserPromptDecision
type UserPromptFunc = engine.UserPromptFunc
type SessionStartEvent = engine.SessionStartEvent
type SessionStartDecision = engine.SessionStartDecision
type SessionStartFunc = engine.SessionStartFunc
type PreCompactEvent = engine.PreCompactEvent
type PreCompactDecision = engine.PreCompactDecision
type PreCompactFunc = engine.PreCompactFunc
type AfterTurnEvent = engine.AfterTurnEvent
type AfterTurnFunc = engine.AfterTurnFunc
type StopEvent = engine.StopEvent
type StopFunc = engine.StopFunc
type Hooks = engine.Hooks

type OTELAutoFunc = engine.OTELAutoFunc
type ProcInfo = engine.ProcInfo
type MediaClient = engine.MediaClient
type MediaImageResult = engine.MediaImageResult
type MediaVideoResult = engine.MediaVideoResult

// SandboxBackend is the OS jail passed to ProcStart (nil/omitted = no jail).
type SandboxBackend = engine.SandboxBackend

// --- Vars / consts ---

var (
	ErrAgentDone          = engine.ErrAgentDone
	ErrAgentMaxTurns      = engine.ErrAgentMaxTurns
	ErrAgentStuck         = engine.ErrAgentStuck
	ProcErrAlreadyRunning = engine.ProcErrAlreadyRunning
)

// Event types (loop / harness bus).
const (
	EventRunStart           = engine.EventRunStart
	EventRunEnd             = engine.EventRunEnd
	EventCompactStart       = engine.EventCompactStart
	EventCompact            = engine.EventCompact
	EventGoalStart          = engine.EventGoalStart
	EventGoalStep           = engine.EventGoalStep
	EventGoalDone           = engine.EventGoalDone
	EventGoalFail           = engine.EventGoalFail
	EventGoalPartial        = engine.EventGoalPartial
	EventGoalBlocked        = engine.EventGoalBlocked
	EventToolStart          = engine.EventToolStart
	EventToolEnd            = engine.EventToolEnd
	EventCompactSummary     = engine.EventCompactSummary
	EventContextSinkStore   = engine.EventContextSinkStore
	EventContextSinkRecover = engine.EventContextSinkRecover
	EventDelegateChunk      = engine.EventDelegateChunk
	EventDelegateProgress   = engine.EventDelegateProgress
	EventDelegateUsage      = engine.EventDelegateUsage
	EventLSPDiagnostics     = engine.EventLSPDiagnostics
	EventSteer              = engine.EventSteer
	// EventModelWait / EventModelActive bracket the pre-first-byte silence of
	// an LLM call. The wait is a host-side observation of upstream silence,
	// never proof the model is reasoning.
	EventModelWait   = engine.EventModelWait
	EventModelActive = engine.EventModelActive
	// EventModelRetry fires once per scheduled LLM retry, before the backoff
	// sleep; Delta carries honest copy ("provider busy · retrying in 3s") that
	// replaces the wait's silence copy while the gateway is not being asked.
	EventModelRetry = engine.EventModelRetry
)

// Retry classifications for RetryInfo.Kind.
const (
	RetryBusy        = engine.RetryBusy
	RetryUnavailable = engine.RetryUnavailable
	RetryNetwork     = engine.RetryNetwork
)

// Misc consts.
const (
	MaxLSPDiagnostics = engine.MaxLSPDiagnostics
	StopCompleted     = engine.StopCompleted
	StopCancelled     = engine.StopCancelled
	StopMaxTurns      = engine.StopMaxTurns
	StopStuck         = engine.StopStuck
	StopBudget        = engine.StopBudget
)

// Version is the fallback release string (overridden by ldflags / git tags).
var Version = engine.Version

// ErrBudget ends a run that hit its spend ceiling (policy.max_run_tokens /
// max_run_usd). Partial history is preserved; this is NOT task completion.
var ErrBudget = engine.ErrBudget

var (
	StopTruncated = engine.StopTruncated
	StopError     = engine.StopError
)

// Diagnostic severities.
const (
	SeverityError       = engine.SeverityError
	SeverityWarning     = engine.SeverityWarning
	SeverityInformation = engine.SeverityInformation
	SeverityHint        = engine.SeverityHint
)

// Compact layers.
const (
	CompactLayerSnip = engine.CompactLayerSnip
	CompactLayerDrop = engine.CompactLayerDrop
)

// --- Functions ---

func New(opt Options) (*Engine, error) { return engine.New(opt) }
func Run(ctx context.Context, prompt string, opt Options) (RunResult, error) {
	return engine.Run(ctx, prompt, opt)
}
func Home() string                 { return engine.Home() }
func VersionString() string        { return engine.VersionString() }
func IsPowerTool(name string) bool { return engine.IsPowerTool(name) }

// BuiltinReadInspectTools are read/glob/grep — the strict allowlist for review/sec prompts.
func BuiltinReadInspectTools() []string { return engine.BuiltinReadInspectTools() }

// SkillsDir returns the global skills directory ($MOW_HOME/skills, default
// ~/.mow/skills) where skills live in the standard <name>/SKILL.md layout.
// It is one of several skill sources: host/user skills.dirs and trusted
// project .mow/skills are also searched. See AvailableSkillNames.
func SkillsDir() string { return engine.SkillsDir() }

func PluginsDir() string { return engine.PluginsDir() }

// AvailableSkillNames returns the sorted, deduplicated skill folder names
// that contain a SKILL.md entry point across the given directories. It lists
// what is discoverable without reading skill bodies into the prompt — hosts
// (e.g. /skill in the TUI) use it to show names so users know what to pass to
// --skill / skills.explicit. Missing dirs are silently skipped.
func AvailableSkillNames(dirs []string) []string { return engine.AvailableSkillNames(dirs) }

// AvailableSkillInfos is AvailableSkillNames plus Agent Skills frontmatter.
func AvailableSkillInfos(dirs []string) []SkillInfo {
	return engine.AvailableSkillInfos(dirs)
}

func ExtractThinking(s string) (visible, thinking string, unclosed bool) {
	return engine.ExtractThinking(s)
}
func StripThinking(s string) (visible, thinking string) { return engine.StripThinking(s) }
func ContextWithEngine(ctx context.Context, eng *Engine) context.Context {
	return engine.ContextWithEngine(ctx, eng)
}
func EngineFromContext(ctx context.Context) *Engine { return engine.EngineFromContext(ctx) }
func SeverityRank(s DiagnosticSeverity) int         { return engine.SeverityRank(s) }
func FilterChatModels(list []ModelInfo) []ModelInfo { return engine.FilterChatModels(list) }
func IsChatModel(m ModelInfo) bool                  { return engine.IsChatModel(m) }
func WorkspaceTrusted(workspace string) bool        { return engine.WorkspaceTrusted(workspace) }
func TrustWorkspace(workspace string) error         { return engine.TrustWorkspace(workspace) }
func RevokeWorkspaceTrust(workspace string) error   { return engine.RevokeWorkspaceTrust(workspace) }
func TrustedWorkspaces() []string                   { return engine.TrustedWorkspaces() }

// SplitExtraRootSpec parses an extra-root spec ("PATH", "PATH:ro", "PATH:rw").
func SplitExtraRootSpec(raw string) (path string, readOnly bool) {
	return engine.SplitExtraRootSpec(raw)
}
func SetOTELAuto(fn OTELAutoFunc)     { engine.SetOTELAuto(fn) }
func ProcSanitizeID(id string) string { return engine.ProcSanitizeID(id) }
func ProcStoreDir(home, workspace string) string {
	return engine.ProcStoreDir(home, workspace)
}
func ProcStart(dir, id, command, logName, workdir string, box ...SandboxBackend) (ProcInfo, error) {
	return engine.ProcStart(dir, id, command, logName, workdir, box...)
}
func ProcStatus(dir, id string) (ProcInfo, error)    { return engine.ProcStatus(dir, id) }
func ProcList(dir string) ([]ProcInfo, error)        { return engine.ProcList(dir) }
func ProcStop(dir, id string) (ProcInfo, error)      { return engine.ProcStop(dir, id) }
func ProcTail(dir, id string, n int) (string, error) { return engine.ProcTail(dir, id, n) }

// NewMediaClient builds a generate/understand client from the chat credential.
// Returns nil when apiKey is empty.
func NewMediaClient(baseURL, apiKey string, extraHeaders map[string]string) *MediaClient {
	return engine.NewMediaClient(baseURL, apiKey, extraHeaders)
}

// MediaClientFromConfig loads llm.base_url, the resolved API key, and
// llm.headers. Returns nil when no key is resolvable (media is opt-in).
func MediaClientFromConfig(configPaths ...string) *MediaClient {
	return engine.MediaClientFromConfig(configPaths...)
}

func MediaDataURL(mime string, data []byte) string { return engine.MediaDataURL(mime, data) }
func MediaMIMEFromPath(p string) string            { return engine.MediaMIMEFromPath(p) }

// WriteWorkspaceFile writes data at rel under workspace through the path jail.
func WriteWorkspaceFile(workspace, rel string, data []byte) (string, error) {
	return engine.WriteWorkspaceFile(workspace, rel, data)
}

// ReadWorkspaceFile reads rel under workspace through the path jail.
func ReadWorkspaceFile(workspace, rel string) (abs string, data []byte, err error) {
	return engine.ReadWorkspaceFile(workspace, rel)
}
