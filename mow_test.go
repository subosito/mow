package mow_test

import (
	"os"
	"testing"

	"github.com/subosito/mow"
)

// mow_test.go verifies the public re-export surface: every type, function,
// and const that mow.go aliases from internal/engine is reachable as mow.*
// This is the only root test — engine behavior tests live in internal/engine/.

// Isolate root tests from the developer's ~/.mow (config, skills, AGENTS).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-home-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestPublicTypesResolve(t *testing.T) {
	// Type aliases carry methods and zero values.
	var _ mow.Engine
	var _ mow.Options
	var _ mow.Message
	var _ mow.Event
	var _ mow.Hooks
	var _ mow.Tool
	var _ mow.RunResult
	var _ mow.ModelInfo
	var _ mow.CompactReport
	var _ mow.PromptOpts
	var _ mow.Usage
	var _ mow.Status
	var _ mow.ProcInfo
	var _ mow.MediaClient
	var _ mow.MediaImageResult
	var _ mow.MediaVideoResult
	var _ mow.ChatHooks
	var _ mow.Provider
	var _ mow.ModelLister
	var _ mow.ModelSwitcher
	var _ mow.ChatFunc
	var _ mow.EventFunc
	var _ mow.OTELAutoFunc
	var _ mow.EventType
	var _ mow.CompactLayer
	var _ mow.DiagnosticSeverity
}

func TestPublicFuncsResolve(t *testing.T) {
	// Functions are reachable as mow.*.
	_ = mow.IsPowerTool
	_ = mow.ExtractThinking
	_ = mow.StripThinking
	_ = mow.ContextWithEngine
	_ = mow.EngineFromContext
	_ = mow.SeverityRank
	_ = mow.FilterChatModels
	_ = mow.IsChatModel
	_ = mow.WorkspaceTrusted
	_ = mow.TrustWorkspace
	_ = mow.RevokeWorkspaceTrust
	_ = mow.TrustedWorkspaces
	_ = mow.SetOTELAuto
	_ = mow.ProcSanitizeID
	_ = mow.ProcStoreDir
	_ = mow.ProcStart
	_ = mow.ProcStatus
	_ = mow.ProcList
	_ = mow.ProcStop
	_ = mow.ProcTail
	_ = mow.NewMediaClient
	_ = mow.MediaClientFromConfig
	_ = mow.MediaDataURL
	_ = mow.MediaMIMEFromPath
	_ = mow.WriteWorkspaceFile
	_ = mow.ReadWorkspaceFile
	_ = mow.Home
	_ = mow.VersionString
	_ = mow.New
	_ = mow.Run
}

func TestPublicConstsResolve(t *testing.T) {
	// Event types.
	_ = mow.EventRunStart
	_ = mow.EventRunEnd
	_ = mow.EventCompactStart
	_ = mow.EventCompact
	_ = mow.EventSteer
	_ = mow.EventGoalStart
	_ = mow.EventGoalStep
	_ = mow.EventGoalDone
	_ = mow.EventGoalFail
	_ = mow.EventGoalPartial
	_ = mow.EventGoalBlocked
	_ = mow.EventToolStart
	_ = mow.EventToolEnd
	_ = mow.EventDelegateChunk
	_ = mow.EventDelegateProgress
	_ = mow.EventDelegateUsage
	_ = mow.EventLSPDiagnostics

	// Stop reasons.
	_ = mow.StopCompleted
	_ = mow.StopCancelled
	_ = mow.StopMaxTurns
	_ = mow.StopStuck
	_ = mow.StopTruncated
	_ = mow.StopError

	// Misc consts.
	_ = mow.MaxLSPDiagnostics
	_ = mow.Version

	// Diagnostic severities.
	_ = mow.SeverityError
	_ = mow.SeverityWarning
	_ = mow.SeverityInformation
	_ = mow.SeverityHint

	// Compact layers.
	_ = mow.CompactLayerSnip
	_ = mow.CompactLayerDrop
}

func TestPublicVarsResolve(t *testing.T) {
	_ = mow.ErrAgentDone
	_ = mow.ErrAgentMaxTurns
	_ = mow.ErrAgentStuck
	_ = mow.ProcErrAlreadyRunning
}

// Type aliases — the receiver method table is inherited.
func TestEngineMethodPresent(t *testing.T) {
	// Compile-time check that Engine has Prompt.
	var eng mow.Engine
	_ = eng.Prompt
	_ = eng.PromptWith
	_ = eng.Close
	_ = eng.Cancel
	_ = eng.Model
	_ = eng.Wire
	_ = eng.Status
	_ = eng.Emit
	_ = eng.Steer
	_ = eng.Compact
	_ = eng.Rewind
	_ = eng.Messages
	_ = eng.Transcript
	_ = eng.AllowWrite
	_ = eng.AllowShell
	_ = eng.SessionID
	_ = eng.Sessions
	_ = eng.Extension
	_ = eng.Workspace
	_ = eng.ExtraRoots
	_ = eng.ResolvePath
	_ = eng.SetModel
	_ = eng.SetWire
	_ = eng.SetEffort
	_ = eng.Effort
	_ = eng.Efforts
	_ = eng.DefaultEffort
	_ = eng.ListModels
	_ = eng.SetModelWithWire
	_ = eng.Limits
	_ = eng.ContextTokens
	_ = eng.AddPreTool
	_ = eng.AddAfterTurn
	_ = eng.AddPostTool
	_ = eng.SetOnToken
	_ = eng.SetOnReasoning
	_ = eng.SetOnEvent
	_ = eng.AddOnEvent
	_ = eng.RegisterCleanup
}
