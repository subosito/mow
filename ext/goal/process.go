package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/subosito/mow"
)

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
		ID      string `json:"id"`
		Command string `json:"command"`
		Log     string `json:"log"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	id := sanitizeProcID(a.ID)
	cmd := strings.TrimSpace(a.Command)
	if id == "" || cmd == "" {
		return "", fmt.Errorf("id and command required")
	}
	dir := procDir(scope.Root, scope.GoalID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Already running?
	if pid, err := readPID(dir, id); err == nil && pidAlive(pid) {
		return fmt.Sprintf("already running id=%s pid=%d", id, pid), nil
	}
	logName := strings.TrimSpace(a.Log)
	if logName == "" {
		logName = id + ".log"
	}
	logName = filepath.Base(logName)
	logPath := filepath.Join(dir, logName)
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	// Detach: new session, no controlling tty; shell -c command.
	c := exec.Command("bash", "-lc", cmd)
	c.Dir = os.Getenv("PWD")
	if wd, err := os.Getwd(); err == nil {
		c.Dir = wd
	}
	c.Stdout = logF
	c.Stderr = logF
	c.Stdin = nil
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		logF.Close()
		return "", err
	}
	pid := c.Process.Pid
	// Release so parent does not wait; process keeps log fd via dup.
	_ = c.Process.Release()
	_ = logF.Close()
	if err := writePID(dir, id, pid); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return "", err
	}
	// Brief settle so callers can connect immediately after.
	time.Sleep(200 * time.Millisecond)
	if !pidAlive(pid) {
		return fmt.Sprintf("started id=%s pid=%d but process already exited — check log %s", id, pid, logPath), nil
	}
	return fmt.Sprintf("started id=%s pid=%d log=%s", id, pid, logPath), nil
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
	var a struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &a)
	dir := procDir(scope.Root, scope.GoalID)
	id := sanitizeProcID(a.ID)
	if id != "" {
		pid, err := readPID(dir, id)
		if err != nil {
			return fmt.Sprintf("id=%s not found", id), nil
		}
		if pidAlive(pid) {
			return fmt.Sprintf("id=%s pid=%d status=running", id, pid), nil
		}
		return fmt.Sprintf("id=%s pid=%d status=dead", id, pid), nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "(no processes)", nil
		}
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".pid") {
			continue
		}
		id := strings.TrimSuffix(name, ".pid")
		pid, err := readPID(dir, id)
		if err != nil {
			continue
		}
		st := "dead"
		if pidAlive(pid) {
			st = "running"
		}
		fmt.Fprintf(&b, "%s pid=%d %s\n", id, pid, st)
	}
	if b.Len() == 0 {
		return "(no processes)", nil
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
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	id := sanitizeProcID(a.ID)
	if id == "" {
		return "", fmt.Errorf("id required")
	}
	dir := procDir(scope.Root, scope.GoalID)
	pid, err := readPID(dir, id)
	if err != nil {
		return fmt.Sprintf("id=%s not found", id), nil
	}
	if pidAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		if pidAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = os.Remove(filepath.Join(dir, id+".pid"))
	return fmt.Sprintf("stopped id=%s pid=%d", id, pid), nil
}

func sanitizeProcID(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func writePID(dir, id string, pid int) error {
	return os.WriteFile(filepath.Join(dir, id+".pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func readPID(dir, id string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(dir, id+".pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}
