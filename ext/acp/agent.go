package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subosito/mow"
)

// AgentOptions configures ACP agent mode over an Engine.
type AgentOptions struct {
	Engine *mow.Engine
	In     io.Reader
	Out    io.Writer
	// Name/version advertised in initialize (defaults: mow / 0.1.0).
	Name    string
	Version string
}

// Agent serves ACP as an *agent* (editor/client → mow).
// Methods: initialize, session/new, session/prompt; notification session/cancel.
func Agent(ctx context.Context, opt AgentOptions) error {
	if opt.Engine == nil {
		return fmt.Errorf("acp: nil engine")
	}
	in := opt.In
	if in == nil {
		in = os.Stdin
	}
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	name := opt.Name
	if name == "" {
		name = "mow"
	}
	ver := opt.Version
	if ver == "" {
		ver = mow.Version
	}

	a := &agentServer{
		eng:  opt.Engine,
		out:  out,
		name: name,
		ver:  ver,
		// sessionId → cancel for in-flight prompt
		cancels: map[string]context.CancelFunc{},
	}
	return a.serve(ctx, in)
}

type agentServer struct {
	eng     *mow.Engine
	out     io.Writer
	name    string
	ver     string
	mu      sync.Mutex
	encMu   sync.Mutex
	cancels map[string]context.CancelFunc
	// ACP session id → state (mode, etc.)
	sessions map[string]*acpSession
	// PTY terminals for terminal/* methods
	terms map[string]*termSession
}

// acpSession is per-editor-session state (not the same as mow session JSONL id).
type acpSession struct {
	mode string // ModeAsk | ModeCode
}

func (a *agentServer) serve(ctx context.Context, in io.Reader) error {
	a.sessions = map[string]*acpSession{}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			a.write(response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: errParse, Message: "parse error"},
			})
			continue
		}
		// notification (no id)
		if _, hasID := msg["id"]; !hasID {
			var n notification
			_ = json.Unmarshal([]byte(line), &n)
			a.handleNotification(n)
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			a.write(response{JSONRPC: "2.0", Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			continue
		}
		a.handleRequest(ctx, req)
	}
	return sc.Err()
}

func (a *agentServer) handleNotification(n notification) {
	if n.Method == "session/cancel" {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(n.Params, &p)
		a.mu.Lock()
		if cancel, ok := a.cancels[p.SessionID]; ok && cancel != nil {
			cancel()
		}
		a.mu.Unlock()
	}
}

func (a *agentServer) handleRequest(parent context.Context, req request) {
	switch req.Method {
	case "initialize":
		a.write(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": ProtocolVersion,
				"agentCapabilities": map[string]any{
					"loadSession": true,
					"promptCapabilities": map[string]any{
						// Media is saved under media/acp/ and referenced in the text prompt.
						"image": true, "audio": true, "embeddedContext": true,
					},
					"mcpCapabilities": map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{
						"list":   map[string]any{},
						"delete": map[string]any{},
						"close":  map[string]any{},
						"resume": map[string]any{},
					},
					"auth": map[string]any{
						"logout": map[string]any{},
					},
				},
				"agentInfo": map[string]any{
					"name": a.name, "version": a.ver,
				},
				"authMethods": []any{},
			},
		})
	case "session/new":
		var p struct {
			Cwd string `json:"cwd"`
		}
		_ = json.Unmarshal(req.Params, &p)
		// Prefer engine session id for continuity.
		sid := a.eng.SessionID()
		if sid == "" {
			sid = "mow-" + fmt.Sprintf("%d", len(a.sessions)+1)
		}
		a.mu.Lock()
		a.sessions[sid] = &acpSession{mode: ModeCode}
		mode := ModeCode
		a.mu.Unlock()
		a.write(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"sessionId": sid,
				"modes":     modeState(mode),
			},
		})
	case "session/load":
		// Resume an existing mow session id (same Engine already holds transcript/prior when constructed with SessionID).
		var p struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "sessionId required"}})
			return
		}
		sid := strings.TrimSpace(p.SessionID)
		a.mu.Lock()
		if a.sessions[sid] == nil {
			a.sessions[sid] = &acpSession{mode: ModeCode}
		}
		mode := a.sessions[sid].mode
		a.mu.Unlock()
		// Stream prior turns as session/update message history (best-effort).
		for _, m := range a.eng.Transcript() {
			a.write(notification{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustJSON(map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "user_message_chunk",
						"content":       map[string]any{"type": "text", "text": m.Role + ": " + m.Content},
					},
				}),
			})
		}
		a.write(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"sessionId": sid,
				"modes":     modeState(mode),
			},
		})
	case "fs/read_text_file":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Path) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "path required"}})
			return
		}
		text, err := a.readWorkspaceFile(p.Path)
		if err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInternal, Message: err.Error()}})
			return
		}
		a.write(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"content": text},
		})
	case "fs/write_text_file":
		// Policy: only when engine has write enabled.
		if !a.eng.AllowWrite() {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "write not enabled"}})
			return
		}
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Path) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "path required"}})
			return
		}
		if err := a.writeWorkspaceFile(p.Path, []byte(p.Content)); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInternal, Message: err.Error()}})
			return
		}
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
	case "session/request_permission", "session/requestPermission":
		// Auto-allow: mow policies already gate tools; editors may still call this.
		var p struct {
			SessionID string `json:"sessionId"`
			ToolCall  any    `json:"toolCall"`
		}
		_ = json.Unmarshal(req.Params, &p)
		a.write(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow"}},
		})
	case "terminal/create":
		var p struct {
			SessionID string   `json:"sessionId"`
			Command   string   `json:"command"`
			Args      []string `json:"args"`
			Cols      int      `json:"cols"`
			Rows      int      `json:"rows"`
		}
		_ = json.Unmarshal(req.Params, &p)
		cols, rows := uint16(80), uint16(24)
		if p.Cols > 0 {
			cols = uint16(p.Cols)
		}
		if p.Rows > 0 {
			rows = uint16(p.Rows)
		}
		t, err := a.createTerminal(p.SessionID, p.Command, p.Args, cols, rows)
		if err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInternal, Message: err.Error()}})
			return
		}
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"terminalId": t.id,
				// Client may still poll terminal/output; live data also arrives as
				// session/update terminal_output / terminal_exit notifications.
				"streaming": true,
			},
		})
	case "terminal/output":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		t := a.getTerm(p.TerminalID)
		if t == nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "unknown terminal"}})
			return
		}
		out := t.takeOutput()
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"output":    out,
				"truncated": false,
				"exitCode":  nilIfRunning(t),
			},
		})
	case "terminal/write":
		var p struct {
			TerminalID string `json:"terminalId"`
			Data       string `json:"data"`
		}
		_ = json.Unmarshal(req.Params, &p)
		t := a.getTerm(p.TerminalID)
		if t == nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "unknown terminal"}})
			return
		}
		if err := t.write([]byte(p.Data)); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInternal, Message: err.Error()}})
			return
		}
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
	case "terminal/resize":
		var p struct {
			TerminalID string `json:"terminalId"`
			Cols       int    `json:"cols"`
			Rows       int    `json:"rows"`
		}
		_ = json.Unmarshal(req.Params, &p)
		t := a.getTerm(p.TerminalID)
		if t == nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "unknown terminal"}})
			return
		}
		cols, rows := uint16(80), uint16(24)
		if p.Cols > 0 {
			cols = uint16(p.Cols)
		}
		if p.Rows > 0 {
			rows = uint16(p.Rows)
		}
		if err := t.resize(cols, rows); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInternal, Message: err.Error()}})
			return
		}
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
	case "terminal/wait_for_exit", "terminal/waitForExit":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		t := a.getTerm(p.TerminalID)
		if t == nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "unknown terminal"}})
			return
		}
		select {
		case <-t.exitCh:
		case <-parent.Done():
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errCancelled, Message: "cancelled"}})
			return
		}
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"exitCode": int(t.code.Load())},
		})
	case "terminal/release":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		a.releaseTerm(p.TerminalID)
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
	case "session/prompt":
		var p promptParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			return
		}
		ws := a.eng.Workspace()
		text, err := materializePrompt(p.Prompt, ws, p.SessionID)
		if err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			return
		}
		if text == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "empty prompt"}})
			return
		}
		ctx, cancel := context.WithCancel(parent)
		a.mu.Lock()
		a.cancels[p.SessionID] = cancel
		mode := ModeCode
		if s := a.sessions[p.SessionID]; s != nil && s.mode != "" {
			mode = s.mode
		} else if a.sessions[p.SessionID] == nil {
			a.sessions[p.SessionID] = &acpSession{mode: ModeCode}
		}
		a.mu.Unlock()
		defer func() {
			cancel()
			a.mu.Lock()
			delete(a.cancels, p.SessionID)
			a.mu.Unlock()
		}()

		// Stream deltas as session/update agent_message_chunk.
		a.eng.SetOnToken(func(d string) {
			if d == "" {
				return
			}
			a.write(notification{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustJSON(sessionUpdateParams{
					SessionID: p.SessionID,
					Update: sessionUpdate{
						SessionUpdate: "agent_message_chunk",
						Content: &struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}{Type: "text", Text: d},
					},
				}),
			})
		})
		popt := mow.PromptOpts{}
		if mode == ModeAsk {
			popt.ReadOnly = true
			popt.SystemAppend = "Session mode is ask (read-only): do not use write, edit, or bash. Prefer read/glob/grep and explanations."
		}
		res, err := a.eng.PromptWith(ctx, text, popt)
		a.eng.SetOnToken(nil)
		if err != nil {
			if ctx.Err() != nil {
				a.write(response{
					JSONRPC: "2.0", ID: req.ID,
					Result: map[string]any{"stopReason": "cancelled"},
				})
				return
			}
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: errInternal, Message: err.Error()},
			})
			return
		}
		// If no streaming happened, emit full text as one chunk for clients that only listen to updates.
		if res.Text != "" {
			// already streamed; still OK
		}
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"stopReason": "end_turn"},
		})
	case "authenticate":
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "logout":
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "session/close":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		sid := strings.TrimSpace(p.SessionID)
		a.mu.Lock()
		if cancel, ok := a.cancels[sid]; ok && cancel != nil {
			cancel()
			delete(a.cancels, sid)
		}
		delete(a.sessions, sid)
		var drop []*termSession
		if a.terms != nil {
			for id, t := range a.terms {
				if t != nil && t.sessionID == sid {
					drop = append(drop, t)
					delete(a.terms, id)
				}
			}
		}
		a.mu.Unlock()
		for _, t := range drop {
			t.release()
		}
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "session/resume":
		var p struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "sessionId required"}})
			return
		}
		sid := strings.TrimSpace(p.SessionID)
		a.mu.Lock()
		if a.sessions[sid] == nil {
			a.sessions[sid] = &acpSession{mode: ModeCode}
		}
		mode := a.sessions[sid].mode
		a.mu.Unlock()
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"sessionId": sid, "modes": modeState(mode)},
		})
	case "session/list":
		a.mu.Lock()
		list := make([]map[string]any, 0, len(a.sessions)+1)
		seen := map[string]bool{}
		for sid := range a.sessions {
			list = append(list, map[string]any{"sessionId": sid, "cwd": a.eng.Workspace(), "title": sid})
			seen[sid] = true
		}
		if engSID := a.eng.SessionID(); engSID != "" && !seen[engSID] {
			list = append(list, map[string]any{"sessionId": engSID, "cwd": a.eng.Workspace(), "title": engSID})
		}
		a.mu.Unlock()
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"sessions": list}})
	case "session/delete":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		sid := strings.TrimSpace(p.SessionID)
		a.mu.Lock()
		if cancel, ok := a.cancels[sid]; ok && cancel != nil {
			cancel()
			delete(a.cancels, sid)
		}
		delete(a.sessions, sid)
		a.mu.Unlock()
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "session/set_mode", "session/setMode":
		var p struct {
			SessionID string `json:"sessionId"`
			ModeID    string `json:"modeId"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "sessionId required"}})
			return
		}
		mode := strings.ToLower(strings.TrimSpace(p.ModeID))
		if mode != ModeAsk && mode != ModeCode {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "modeId must be ask or code"}})
			return
		}
		sid := strings.TrimSpace(p.SessionID)
		a.mu.Lock()
		if a.sessions[sid] == nil {
			a.sessions[sid] = &acpSession{mode: mode}
		} else {
			a.sessions[sid].mode = mode
		}
		a.mu.Unlock()
		// Notify client of mode change (optional but useful for UI sync).
		a.write(notification{
			JSONRPC: "2.0",
			Method:  "session/update",
			Params: mustJSON(map[string]any{
				"sessionId": sid,
				"update": map[string]any{
					"sessionUpdate": "current_mode_update",
					"currentModeId": mode,
				},
			}),
		})
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "session/set_config_option", "session/setConfigOption":
		// No config options advertised; accept for forward compatibility.
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"configOptions": []any{}}})
	case "terminal/kill":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		t := a.getTerm(p.TerminalID)
		if t == nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "unknown terminal"}})
			return
		}
		t.kill()
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	default:
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errMethod, Message: "method not found: " + req.Method},
		})
	}
}

func (a *agentServer) write(v any) {
	if a.out == nil {
		return
	}
	a.encMu.Lock()
	defer a.encMu.Unlock()
	enc := json.NewEncoder(a.out)
	_ = enc.Encode(v)
}

func (a *agentServer) jailPath(p string) (full string, err error) {
	ws := a.eng.Workspace()
	full = p
	if !filepath.IsAbs(full) {
		full = filepath.Join(ws, full)
	}
	rel, err := filepath.Rel(ws, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path outside workspace")
	}
	return full, nil
}

func (a *agentServer) readWorkspaceFile(p string) (string, error) {
	full, err := a.jailPath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	const maxN = 256 << 10
	if len(data) > maxN {
		return string(data[:maxN]) + "\n…(truncated)", nil
	}
	return string(data), nil
}

func (a *agentServer) writeWorkspaceFile(p string, data []byte) error {
	full, err := a.jailPath(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func nilIfRunning(t *termSession) any {
	if t == nil || !t.closed.Load() {
		return nil
	}
	return int(t.code.Load())
}
