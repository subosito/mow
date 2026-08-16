// Package proc is the background-process pack: proc_start / proc_status /
// proc_stop tools plus a `mow proc` CLI, so an agent can launch a long-lived
// process (dev server, watcher, mock) and keep working while it runs — the
// start tool returns a pid immediately and the process is detached. Available
// anywhere (run/repl/host), gated by --allow-shell since it runs shell
// commands. Blank-import to link. Shares internal/proc with ext/goal.
package proc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	iproc "github.com/subosito/mow/internal/proc"
)

func init() {
	ext.RegisterTool(startTool{})
	ext.RegisterTool(statusTool{})
	ext.RegisterTool(stopTool{})
	ext.RegisterCommand(ext.Command{
		Name:    "proc",
		Summary: "Background processes — list | stop | logs",
		Layer:   "ext",
		Run:     procCmd,
	})
}

// storeDir is $MOW_HOME/proc/<workspace-hash> — per-project, so processes from
// different repos don't collide. The `mow proc` CLI resolves the same dir from
// the current directory.
func storeDir(workspace string) string {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	sum := sha256.Sum256([]byte(ws))
	return filepath.Join(mow.Home(), "proc", hex.EncodeToString(sum[:6]))
}

// requireShell resolves the engine and enforces --allow-shell (background
// processes run arbitrary shell commands — same trust bar as bash).
func requireShell(ctx context.Context) (*mow.Engine, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return nil, errors.New("error: proc tools need the engine context")
	}
	if !eng.AllowShell() {
		return nil, errors.New("error: proc_* requires --allow-shell (it runs shell commands)")
	}
	return eng, nil
}

type startTool struct{}

func (startTool) Name() string { return "proc_start" }
func (startTool) Description() string {
	return "Start a long-lived process in the background (dev server, watcher, mock) and keep working while it runs — this returns a pid immediately; the process is detached. Args: id (short name), command (shell), log (optional filename), keep (bool: survive session exit; default false = auto-killed on exit). Manage with proc_status / proc_stop. Requires --allow-shell. Do NOT use bare `bash &` for servers: the bash tool kills its process group when it returns."
}
func (startTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"command":{"type":"string"},"log":{"type":"string"},"keep":{"type":"boolean"}},"required":["id","command"]}`)
}
func (startTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng, err := requireShell(ctx)
	if err != nil {
		return err.Error(), nil
	}
	var a struct {
		ID, Command, Log string
		Keep             bool
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	dir := storeDir(eng.Workspace())
	info, err := iproc.Start(dir, a.ID, a.Command, a.Log, eng.Workspace())
	if errors.Is(err, iproc.ErrAlreadyRunning) {
		return fmt.Sprintf("already running id=%s pid=%d", info.ID, info.PID), nil
	}
	if err != nil {
		return "error: " + err.Error(), nil
	}
	if !info.Alive {
		return fmt.Sprintf("started id=%s pid=%d but it exited immediately — check log %s", info.ID, info.PID, info.Log), nil
	}
	// Auto-kill on session exit unless keep=true. Cleanup runs on Engine.Close().
	if !a.Keep {
		id := info.ID
		eng.RegisterCleanup(func() { _, _ = iproc.Stop(dir, id) })
		return fmt.Sprintf("started id=%s pid=%d log=%s", info.ID, info.PID, info.Log), nil
	}
	return fmt.Sprintf("started id=%s pid=%d log=%s (kept — survives session exit)", info.ID, info.PID, info.Log), nil
}

type statusTool struct{}

func (statusTool) Name() string   { return "proc_status" }
func (statusTool) ReadOnly() bool { return true }
func (statusTool) Description() string {
	return "Status of background processes. Args: id (from proc_start; omit to list all). For a single id, includes a tail of its log."
}
func (statusTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)
}
func (statusTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng, err := requireShell(ctx)
	if err != nil {
		return err.Error(), nil
	}
	var a struct{ ID string }
	_ = json.Unmarshal(args, &a)
	dir := storeDir(eng.Workspace())
	if id := iproc.SanitizeID(a.ID); id != "" {
		info, err := iproc.Status(dir, id)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		out := fmt.Sprintf("id=%s pid=%d status=%s", info.ID, info.PID, procState(info.Alive))
		if tail, _ := iproc.Tail(dir, id, 20); tail != "" {
			out += "\n--- log (tail) ---\n" + tail
		}
		return out, nil
	}
	list, _ := iproc.List(dir)
	if len(list) == 0 {
		return "(no background processes)", nil
	}
	var b strings.Builder
	for _, p := range list {
		fmt.Fprintf(&b, "%s pid=%d %s\n", p.ID, p.PID, procState(p.Alive))
	}
	return strings.TrimSpace(b.String()), nil
}

type stopTool struct{}

func (stopTool) Name() string { return "proc_stop" }
func (stopTool) Description() string {
	return "Stop a background process (SIGTERM then SIGKILL). Args: id (required)."
}
func (stopTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (stopTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng, err := requireShell(ctx)
	if err != nil {
		return err.Error(), nil
	}
	var a struct{ ID string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	id := iproc.SanitizeID(a.ID)
	if id == "" {
		return "error: id required", nil
	}
	info, err := iproc.Stop(storeDir(eng.Workspace()), id)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	return fmt.Sprintf("stopped id=%s pid=%d", info.ID, info.PID), nil
}

func procState(alive bool) string {
	if alive {
		return "running"
	}
	return "dead"
}

// procCmd is `mow proc …` — manage the current project's background processes.
func procCmd(args []string) int {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		printProcUsage()
		return 0
	}
	dir := storeDir("") // current directory's project
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "ls":
		list, _ := iproc.List(dir)
		if len(list) == 0 {
			fmt.Println("(no background processes)")
			return 0
		}
		for _, p := range list {
			fmt.Printf("%-24s pid=%-8d %s\n", p.ID, p.PID, procState(p.Alive))
		}
		return 0
	case "stop":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "mow proc stop: id required")
			fmt.Fprintln(os.Stderr, "  mow proc stop <id>")
			return 2
		}
		info, err := iproc.Stop(dir, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "mow proc:", err)
			return 1
		}
		fmt.Printf("stopped %s (pid %d)\n", info.ID, info.PID)
		return 0
	case "stop-all":
		list, _ := iproc.List(dir)
		n := 0
		for _, p := range list {
			if _, err := iproc.Stop(dir, p.ID); err == nil {
				n++
			}
		}
		fmt.Printf("stopped %d process(es)\n", n)
		return 0
	case "logs", "log":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "mow proc logs: id required")
			fmt.Fprintln(os.Stderr, "  mow proc logs <id> [lines]")
			return 2
		}
		n := 40
		if len(args) >= 3 {
			if v, err := strconv.Atoi(args[2]); err == nil && v > 0 {
				n = v
			}
		}
		out, err := iproc.Tail(dir, args[1], n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mow proc:", err)
			return 1
		}
		if strings.TrimSpace(out) == "" {
			fmt.Println("(no log)")
		} else {
			fmt.Println(out)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mow proc: unknown %q\n\n", sub)
		printProcUsage()
		return 2
	}
}

func printProcUsage() {
	fmt.Fprintf(os.Stderr, `mow proc — background processes for this workspace

State: $MOW_HOME/proc/<workspace-hash>/
Started by the agent via proc_start (requires --allow-shell).

Commands:

  mow proc list                 list processes (default)
  mow proc stop <id>            stop one (SIGTERM then SIGKILL)
  mow proc stop-all             stop every process in this workspace
  mow proc logs <id> [n]        tail log (default 40 lines)

Examples:

  mow proc
  mow proc stop dev-server
  mow proc logs dev-server 80

Tools (agent): proc_start, proc_status, proc_stop
`)
}
