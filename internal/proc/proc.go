// Package proc manages long-lived background processes: start detached, record
// a pid + logfile, check status, tail logs, and stop. Shared by ext/proc (the
// general proc_* tools + `mow proc` CLI) and ext/goal (goal-scoped process
// tools). Detach model: new session (Setsid) + Release so the parent never
// waits — the process outlives the tool call and the agent loop continues.
package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrAlreadyRunning is returned by Start when id is already alive.
var ErrAlreadyRunning = errors.New("proc: already running")

// Info describes one managed process.
type Info struct {
	ID    string
	PID   int
	Log   string
	Alive bool
}

// SanitizeID keeps a filesystem-safe short id.
func SanitizeID(id string) string {
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

// Start launches command via `bash -lc` detached under dir, logging to
// <dir>/<logName> (default <id>.log), and records <dir>/<id>.pid. Returns
// ErrAlreadyRunning (with the live Info) when id is already running.
func Start(dir, id, command, logName, workdir string) (Info, error) {
	id = SanitizeID(id)
	command = strings.TrimSpace(command)
	if id == "" || command == "" {
		return Info{}, fmt.Errorf("id and command required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Info{}, err
	}
	if pid, err := readPID(dir, id); err == nil && pidAlive(pid) {
		return Info{ID: id, PID: pid, Alive: true}, ErrAlreadyRunning
	}
	if strings.TrimSpace(logName) == "" {
		logName = id + ".log"
	}
	logPath := filepath.Join(dir, filepath.Base(logName))
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Info{}, err
	}
	c := exec.Command("bash", "-lc", command)
	if strings.TrimSpace(workdir) != "" {
		c.Dir = workdir
	}
	c.Stdout = logF
	c.Stderr = logF
	c.Stdin = nil
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detached session, no tty
	if err := c.Start(); err != nil {
		logF.Close()
		return Info{}, err
	}
	pid := c.Process.Pid
	_ = c.Process.Release() // parent must not wait; process keeps the log fd
	_ = logF.Close()
	if err := writePID(dir, id, pid); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return Info{}, err
	}
	time.Sleep(200 * time.Millisecond) // brief settle so callers can connect
	return Info{ID: id, PID: pid, Log: logPath, Alive: pidAlive(pid)}, nil
}

// Status returns the info for one id (error if unknown).
func Status(dir, id string) (Info, error) {
	id = SanitizeID(id)
	pid, err := readPID(dir, id)
	if err != nil {
		return Info{}, fmt.Errorf("id %q not found", id)
	}
	return Info{ID: id, PID: pid, Log: logPathFor(dir, id), Alive: pidAlive(pid)}, nil
}

// List returns every recorded process in dir (empty when none).
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
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
		out = append(out, Info{ID: id, PID: pid, Log: logPathFor(dir, id), Alive: pidAlive(pid)})
	}
	return out, nil
}

// Stop SIGTERMs then (if still alive) SIGKILLs id and removes its pid file.
func Stop(dir, id string) (Info, error) {
	id = SanitizeID(id)
	pid, err := readPID(dir, id)
	if err != nil {
		return Info{}, fmt.Errorf("id %q not found", id)
	}
	alive := pidAlive(pid)
	if alive {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		if pidAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = os.Remove(filepath.Join(dir, id+".pid"))
	return Info{ID: id, PID: pid, Alive: false}, nil
}

// Tail returns the last n lines of id's log ("" when no log yet).
func Tail(dir, id string, n int) (string, error) {
	raw, err := os.ReadFile(logPathFor(dir, SanitizeID(id)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func logPathFor(dir, id string) string { return filepath.Join(dir, id+".log") }

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
	return syscall.Kill(pid, 0) == nil
}
