// Package proc manages long-lived background processes: start detached, record
// a pid + logfile, check status, tail logs, and stop. Shared by ext/proc (the
// general proc_* tools + `mow proc` CLI) and ext/goal (goal-scoped process
// tools). Detach model: new session (Setsid) + Release so the parent never
// waits — the process outlives the tool call and the agent loop continues.
package proc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/subosito/mow/internal/sandbox"
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

// StoreDir is $MOW_HOME/proc/<workspace-hash> — per-project, so processes from
// different repos do not collide. Shared by ext/proc tools, `mow proc`, and
// RPC status so the TUI lists the same store the agent started.
func StoreDir(home, workspace string) string {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	sum := sha256.Sum256([]byte(ws))
	home = strings.TrimSpace(home)
	if home == "" {
		home = "."
	}
	return filepath.Join(home, "proc", hex.EncodeToString(sum[:6]))
}

// Start launches command via `bash -lc` detached under dir, logging to
// <dir>/<logName> (default <id>.log), and records <dir>/<id>.pid. Returns
// ErrAlreadyRunning (with the live Info) when id is already running.
//
// The optional box wraps the command in an OS sandbox (--sandbox=bwrap).
// proc_start must honor the same jail as the bash tool: a sandboxed bash next
// to an unsandboxed proc_start is just a slower escape hatch. Setsid stays in
// the parent so the recorded pid is still the session leader we later signal —
// the backend is asked not to add its own --new-session.
func Start(dir, id, command, logName, workdir string, box ...sandbox.Backend) (Info, error) {
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
	if len(box) > 0 && box[0] != nil {
		wrapped, werr := sandbox.WithNewSession(box[0], false).Wrap(c)
		if werr != nil {
			logF.Close()
			return Info{}, werr
		}
		wrapped.Stdout = logF
		wrapped.Stderr = logF
		wrapped.Stdin = nil
		c = wrapped
	}
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
	waitStarted(logPath, pid)
	return Info{ID: id, PID: pid, Log: logPath, Alive: pidAlive(pid)}, nil
}

// startSettleTimeout bounds the wait for a freshly spawned process to produce
// its first output. Short on purpose: it only has to cover fork+exec of
// `bash -lc`, not the process becoming useful. Plenty of legitimate processes
// (servers, `sleep`) print nothing for a long time, and they must not pay this
// cost on every start.
const startSettleTimeout = 750 * time.Millisecond

// waitStarted gives a just-spawned process a moment to get going, so callers
// that immediately read the log or the status tail see output rather than an
// empty file.
//
// This replaces a fixed 200ms sleep, which was simultaneously too long on an
// idle machine and too short on a loaded one: CI runners fork `bash -lc` far
// slower than a developer box, so an immediate log read raced the child and
// TestLogWritingAndTail failed only on CI. Poll instead — return the moment the
// log has bytes or the process is gone, and cap the wait so a deliberately
// silent process still starts promptly.
func waitStarted(logPath string, pid int) {
	deadline := time.Now().Add(startSettleTimeout)
	for {
		if fi, err := os.Stat(logPath); err == nil && fi.Size() > 0 {
			return // produced output: definitely running
		}
		if !pidAlive(pid) {
			return // exited already; caller reports it via Alive
		}
		if time.Now().After(deadline) {
			return // silent but alive (e.g. a server with no banner)
		}
		time.Sleep(2 * time.Millisecond)
	}
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

// Stop SIGTERMs then (if still alive) SIGKILLs the process group for id
// (Setsid session leader) and removes its pid file. Falls back to the single
// PID if the group signal fails.
func Stop(dir, id string) (Info, error) {
	id = SanitizeID(id)
	pid, err := readPID(dir, id)
	if err != nil {
		return Info{}, fmt.Errorf("id %q not found", id)
	}
	alive := pidAlive(pid)
	if alive {
		// Process was started with Setsid: negative PID targets the session/group.
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		if pidAlive(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = os.Remove(filepath.Join(dir, id+".pid"))
	return Info{ID: id, PID: pid, Alive: false}, nil
}

// maxTailBytes caps how much of a log file is read for Tail (then line-sliced).
const maxTailBytes = 1 << 20 // 1 MiB

// Tail returns the last n lines of id's log ("" when no log yet).
// Only the last maxTailBytes of the file are considered so a huge log cannot
// fill memory when the agent polls proc_status.
func Tail(dir, id string, n int) (string, error) {
	path := logPathFor(dir, SanitizeID(id))
	raw, err := readTailBytes(path, maxTailBytes)
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

func readTailBytes(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size <= 0 {
		return nil, nil
	}
	if max <= 0 {
		max = maxTailBytes
	}
	var start int64
	if size > int64(max) {
		start = size - int64(max)
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, err
	}
	buf := make([]byte, size-start)
	_, err = io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	// If we skipped a prefix, drop a partial first line.
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
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
