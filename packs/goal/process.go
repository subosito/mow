package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/subosito/mow"
)

// Goal-scoped background process tools. The mechanism is re-exported from
// internal/proc via the public mow.Proc* API (packs/ lives in a separate
// Go module and cannot import internal/). These tools scope storage to the
// goal dir and gate on an active goal.

// processScope is carried on the step context for process tools.
type processScope struct {
	GoalID string
	Root   string // goals store dir
}

type processScopeKey struct{}

func withProcessScope(ctx context.Context, s processScope) context.Context {
	return context.WithValue(ctx, processScopeKey{}, s)
}

func processScopeFrom(ctx context.Context) (processScope, bool) {
	v, ok := ctx.Value(processScopeKey{}).(processScope)
	return v, ok && strings.TrimSpace(v.GoalID) != ""
}

func procDir(root, goalID string) string {
	return filepath.Join(root, goalID, "procs")
}

func procState(alive bool) string {
	if alive {
		return "running"
	}
	return "dead"
}

// ProcessTools returns goal-scoped process lifecycle tools for ExtraTools.
func ProcessTools() []mow.Tool {
	return []mow.Tool{procStartTool{}, procStatusTool{}, procStopTool{}}
}

type procStartTool struct{}

func (procStartTool) Name() string { return "goal_process_start" }
func (procStartTool) Description() string {
	return "Start a long-lived process in the background for this goal (server, mock, etc.). " +
		"Args: id (short name), command (shell), optional log name. Returns pid. " +
		"Use goal_process_status / goal_process_stop to manage it. Do not use bare bash & for servers."
}
func (procStartTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"command":{"type":"string"},"log":{"type":"string"}},"required":["id","command"]}`)
}
func (procStartTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	scope, ok := processScopeFrom(ctx)
	if !ok {
		return "goal_process_start ignored (no active goal)", nil
	}
	var a struct {
		ID, Command, Log string
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	info, err := mow.ProcStart(procDir(scope.Root, scope.GoalID), a.ID, a.Command, a.Log, "")
	if errors.Is(err, mow.ProcErrAlreadyRunning) {
		return fmt.Sprintf("already running id=%s pid=%d", info.ID, info.PID), nil
	}
	if err != nil {
		return "", err
	}
	if !info.Alive {
		return fmt.Sprintf("started id=%s pid=%d but process already exited — check log %s", info.ID, info.PID, info.Log), nil
	}
	return fmt.Sprintf("started id=%s pid=%d log=%s", info.ID, info.PID, info.Log), nil
}

type procStatusTool struct{}

func (procStatusTool) Name() string { return "goal_process_status" }
func (procStatusTool) Description() string {
	return "Status of a goal background process. Args: id (from goal_process_start). Omit id to list all."
}
func (procStatusTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)
}
func (procStatusTool) ReadOnly() bool { return true }
func (procStatusTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	scope, ok := processScopeFrom(ctx)
	if !ok {
		return "goal_process_status ignored (no active goal)", nil
	}
	var a struct{ ID string }
	_ = json.Unmarshal(args, &a)
	dir := procDir(scope.Root, scope.GoalID)
	if id := mow.ProcSanitizeID(a.ID); id != "" {
		info, err := mow.ProcStatus(dir, id)
		if err != nil {
			return fmt.Sprintf("id=%s not found", id), nil
		}
		return fmt.Sprintf("id=%s pid=%d status=%s", info.ID, info.PID, procState(info.Alive)), nil
	}
	list, err := mow.ProcList(dir)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "(no processes)", nil
	}
	var b strings.Builder
	for _, p := range list {
		fmt.Fprintf(&b, "%s pid=%d %s\n", p.ID, p.PID, procState(p.Alive))
	}
	return strings.TrimSpace(b.String()), nil
}

type procStopTool struct{}

func (procStopTool) Name() string { return "goal_process_stop" }
func (procStopTool) Description() string {
	return "Stop a goal background process. Args: id (required)."
}
func (procStopTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (procStopTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	scope, ok := processScopeFrom(ctx)
	if !ok {
		return "goal_process_stop ignored (no active goal)", nil
	}
	var a struct{ ID string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	id := mow.ProcSanitizeID(a.ID)
	if id == "" {
		return "", fmt.Errorf("id required")
	}
	info, err := mow.ProcStop(procDir(scope.Root, scope.GoalID), id)
	if err != nil {
		return fmt.Sprintf("id=%s not found", id), nil
	}
	return fmt.Sprintf("stopped id=%s pid=%d", info.ID, info.PID), nil
}
