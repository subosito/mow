package acp

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

// termSession is a real PTY-backed shell for ACP terminal/* methods.
type termSession struct {
	id        string
	sessionID string // ACP session for push notifications
	cmd       *exec.Cmd
	ptmx      *os.File
	mu        sync.Mutex
	buf       bytes.Buffer // for pull (terminal/output)
	closed    atomic.Bool
	exitCh    chan struct{}
	code      atomic.Int32

	// push: live output / exit to the client via session/update
	pushOutput func(termID, sessionID, data string)
	pushExit   func(termID, sessionID string, code int)
}

func (a *agentServer) createTerminal(sessionID, command string, args []string, cols, rows uint16) (*termSession, error) {
	if !a.eng.AllowShell() {
		return nil, fmt.Errorf("shell not enabled (allow_shell / --allow-shell)")
	}
	a.mu.Lock()
	if s := a.sessions[sessionID]; s != nil && s.mode == ModeAsk {
		a.mu.Unlock()
		return nil, fmt.Errorf("terminal denied: session mode is ask (read-only)")
	}
	a.mu.Unlock()
	ws := a.eng.Workspace()
	if command == "" {
		command = "bash"
		args = []string{"-l"}
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = ws
	cmd.Env = os.Environ()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("term-%d", time.Now().UnixNano())
	t := &termSession{
		id: id, sessionID: sessionID, cmd: cmd, ptmx: ptmx, exitCh: make(chan struct{}),
		pushOutput: a.pushTerminalOutput,
		pushExit:   a.pushTerminalExit,
	}
	go t.readLoop()
	go t.waitLoop()
	a.mu.Lock()
	if a.terms == nil {
		a.terms = map[string]*termSession{}
	}
	a.terms[id] = t
	a.mu.Unlock()
	return t, nil
}

func (a *agentServer) pushTerminalOutput(termID, sessionID, data string) {
	if data == "" {
		return
	}
	// session/update with terminal_output chunk (practical ACP extension shape).
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "terminal_output",
				"terminalId":    termID,
				"data":          data,
			},
		}),
	})
}

func (a *agentServer) pushTerminalExit(termID, sessionID string, code int) {
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "terminal_exit",
				"terminalId":    termID,
				"exitCode":      code,
			},
		}),
	})
}

func (t *termSession) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			t.mu.Lock()
			const maxBuf = 512 << 10
			if t.buf.Len()+n > maxBuf {
				b := t.buf.Bytes()
				half := len(b) / 2
				t.buf.Reset()
				t.buf.Write(b[half:])
			}
			t.buf.Write(buf[:n])
			t.mu.Unlock()
			if t.pushOutput != nil {
				t.pushOutput(t.id, t.sessionID, chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *termSession) waitLoop() {
	err := t.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	t.code.Store(int32(code))
	t.closed.Store(true)
	if t.pushExit != nil {
		t.pushExit(t.id, t.sessionID, code)
	}
	close(t.exitCh)
}

func (t *termSession) takeOutput() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.buf.String()
	t.buf.Reset()
	return s
}

func (t *termSession) write(data []byte) error {
	if t.closed.Load() {
		return io.ErrClosedPipe
	}
	_, err := t.ptmx.Write(data)
	return err
}

func (t *termSession) resize(cols, rows uint16) error {
	if t.closed.Load() {
		return io.ErrClosedPipe
	}
	return pty.Setsize(t.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

func (t *termSession) kill() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}

func (t *termSession) release() {
	_ = t.closed.Swap(true)
	_ = t.ptmx.Close()
	t.kill()
	select {
	case <-t.exitCh:
	case <-time.After(2 * time.Second):
	}
}

func (a *agentServer) getTerm(id string) *termSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terms == nil {
		return nil
	}
	return a.terms[id]
}

func (a *agentServer) releaseTerm(id string) {
	a.mu.Lock()
	t := a.terms[id]
	delete(a.terms, id)
	a.mu.Unlock()
	if t != nil {
		t.release()
	}
}
