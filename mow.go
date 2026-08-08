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
)

// Misc consts.
const (
	MaxLSPDiagnostics = engine.MaxLSPDiagnostics
	Version           = engine.Version
	StopCompleted     = engine.StopCompleted
	StopCancelled     = engine.StopCancelled
	StopMaxTurns      = engine.StopMaxTurns
	StopStuck         = engine.StopStuck
	StopTruncated     = engine.StopTruncated
	StopError         = engine.StopError
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
func ProcStart(dir, id, command, logName, workdir string) (ProcInfo, error) {
	return engine.ProcStart(dir, id, command, logName, workdir)
}
func ProcStatus(dir, id string) (ProcInfo, error)    { return engine.ProcStatus(dir, id) }
func ProcList(dir string) ([]ProcInfo, error)        { return engine.ProcList(dir) }
func ProcStop(dir, id string) (ProcInfo, error)      { return engine.ProcStop(dir, id) }
func ProcTail(dir, id string, n int) (string, error) { return engine.ProcTail(dir, id, n) }
