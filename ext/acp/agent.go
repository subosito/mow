package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subosito/mow"
	"github.com/subosito/mow/internal/policy"
	toolspkg "github.com/subosito/mow/internal/tools"
)

// AgentOptions configures ACP agent mode over an Engine.
type AgentOptions struct {
	Engine *mow.Engine
	In     io.Reader
	Out    io.Writer
	// Name/version advertised in initialize (defaults: mow / mow.Version).
	Name    string
	Version string
}

// Agent serves ACP as an *agent* (editor/client → mow).
// Core: initialize, one session/new (or load), session/prompt, session/cancel.
// Completeness: session/load|list|resume|close|delete, set_mode,
// set_config_option, available_commands_update, session/request_permission
// (agent→client), usage_update, slash dispatch, terminals.
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
		cancels:      map[string]context.CancelFunc{},
		pending:      map[string]chan incomingResponse{},
		alwaysAllow:  map[string]bool{},
		alwaysReject: map[string]bool{},
	}
	unsub := a.eng.AddPreTool(a.preTool)
	defer unsub()
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

	nextID       atomic.Int64
	pending      map[string]chan incomingResponse
	alwaysAllow  map[string]bool
	alwaysReject map[string]bool
	// activeSID is the in-flight prompt session (cleared when the turn ends).
	activeSID string
	// boundSID is the one ACP session this process owns. Empty means unbound.
	boundSID string
	// freshAfterClose: next session/new must BeginSession (not rebind JSONL).
	freshAfterClose bool
	sessionSeq      atomic.Int64
}

// acpSession is ACP mode/approvals for the bound session.
type acpSession struct {
	mode      string // ModeAsk | ModeCode
	approvals string // ApprovalPrompt | ApprovalAlways
}

func (a *agentServer) serve(ctx context.Context, in io.Reader) error {
	a.sessions = map[string]*acpSession{}
	var wg sync.WaitGroup
	defer wg.Wait()
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
		// Incoming JSON-RPC: notification (no id), client response to an
		// agent→client request (id, no method), or a client request.
		if _, hasID := msg["id"]; !hasID {
			var n notification
			_ = json.Unmarshal([]byte(line), &n)
			a.handleNotification(n)
			continue
		}
		if _, hasMethod := msg["method"]; !hasMethod {
			a.deliverClientResponse([]byte(line))
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			a.write(response{JSONRPC: "2.0", Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			continue
		}
		switch req.Method {
		case "session/prompt", "session/load", "terminal/wait_for_exit", "terminal/waitForExit", "compact", "slash", "rewind", "skill.activate", "transcript":
			// Long-blocking methods run off the read loop so session/cancel
			// (and other traffic) is still read while they are in flight.
			// compact rewrites in-memory history and can take long enough that
			// a blocked stdin read looks like a dead agent to the TUI.
			wg.Add(1)
			go func(req request) {
				defer wg.Done()
				defer func() {
					if rec := recover(); rec != nil {
						a.write(response{
							JSONRPC: "2.0", ID: req.ID,
							Error: &rpcError{Code: errInternal, Message: fmt.Sprintf("panic: %v", rec)},
						})
					}
				}()
				a.handleRequest(ctx, req)
			}(req)
		default:
			a.handleRequest(ctx, req)
		}
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
					// Optional mow extras (same connection). Generic clients ignore.
					"experimental": extraCapabilities(),
					"extras":       extraMethodNames(),
				},
				"agentInfo": map[string]any{
					"name": a.name, "version": a.ver,
				},
				"authMethods": []any{},
			},
		})
	case "session/new":
		a.handleSessionNew(parent, req)
	case "session/load":
		a.handleSessionLoad(parent, req, true)
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
		text = expandPromptFileRefs(a.eng, text)
		if err := a.requireBound(p.SessionID); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			return
		}
		ctx, cancel := context.WithCancel(parent)
		a.mu.Lock()
		a.cancels[p.SessionID] = cancel
		a.activeSID = p.SessionID
		mode := ModeCode
		if s := a.sessions[p.SessionID]; s != nil && s.mode != "" {
			mode = s.mode
		}
		a.mu.Unlock()
		defer func() {
			cancel()
			a.mu.Lock()
			delete(a.cancels, p.SessionID)
			if a.activeSID == p.SessionID {
				a.activeSID = ""
			}
			a.mu.Unlock()
		}()

		if body, exclusiveBusy, ok := a.trySlash(ctx, p.SessionID, text); ok {
			if exclusiveBusy {
				a.write(response{
					JSONRPC: "2.0", ID: req.ID,
					Error: &rpcError{Code: errInvalid, Message: "exclusive slash command cannot run while a turn is in flight"},
				})
				return
			}
			if body != "" {
				a.write(notification{
					JSONRPC: "2.0",
					Method:  "session/update",
					Params: mustJSON(sessionUpdateParams{
						SessionID: p.SessionID,
						Update: sessionUpdate{
							SessionUpdate: "agent_message_chunk",
							Content:       &chunkContent{Type: "text", Text: body},
						},
					}),
				})
			}
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Result: map[string]any{"stopReason": "end_turn"},
			})
			return
		}

		// Stream main-model tokens + delegate peer activity to the client.
		var streamed atomic.Bool
		writeAgentText := func(text string) {
			if text == "" {
				return
			}
			streamed.Store(true)
			a.write(notification{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustJSON(sessionUpdateParams{
					SessionID: p.SessionID,
					Update: sessionUpdate{
						SessionUpdate: "agent_message_chunk",
						Content:       &chunkContent{Type: "text", Text: text},
					},
				}),
			})
		}
		// writeToolCall emits ACP tool_call / tool_call_update so hosts can show
		// "read engine.go" / "grep foo in pkg/" instead of bare "→ read".
		writeToolCall := func(updateKind, callID, kind, title, status string, args json.RawMessage, result string) {
			if kind == "" && title == "" {
				return
			}
			update := map[string]any{
				"sessionUpdate": updateKind,
				"toolCallId":    callID,
				"kind":          kind,
				"title":         title,
				"status":        status,
			}
			if len(args) > 0 && string(args) != "null" {
				update["rawInput"] = json.RawMessage(args)
			}
			if result != "" {
				update["rawOutput"] = result
				update["content"] = []map[string]any{
					{"type": "content", "content": map[string]any{"type": "text", "text": result}},
				}
			}
			a.emitSessionUpdate(p.SessionID, update)
		}
		a.eng.SetOnToken(writeAgentText)
		// Fan-in peer events while Prompt runs (does not replace host listeners).
		unsub := a.eng.AddOnEvent(func(ev mow.Event) {
			switch ev.Type {
			case mow.EventDelegateChunk:
				// A nested delegate's answer is NOT this agent's reply.
				// Forward as an extra update so mowi /peers can paint it
				// without committing it as the host answer.
				agent := strings.TrimSpace(ev.Agent)
				line := strings.TrimSpace(ev.Delta)
				if line != "" {
					a.emitSessionUpdate(p.SessionID, map[string]any{
						"sessionUpdate": "delegate_chunk",
						"agent":         agent,
						"delta":         line,
					})
				}
			case mow.EventDelegateProgress:
				agent := strings.TrimSpace(ev.Agent)
				line := strings.TrimSpace(ev.Delta)
				if line == "" {
					return
				}
				a.emitSessionUpdate(p.SessionID, map[string]any{
					"sessionUpdate": "delegate_progress",
					"agent":         agent,
					"phase":         line,
				})
			case mow.EventToolStart:
				kind, title := toolCallKindTitle(ev.Tool, ev.Args)
				writeToolCall("tool_call", ev.ToolCallID, kind, title, "in_progress", ev.Args, "")
			case mow.EventToolEnd:
				kind, title := toolCallKindTitle(ev.Tool, ev.Args)
				status := "completed"
				if ev.Denied || strings.TrimSpace(ev.Error) != "" {
					status = "failed"
				}
				writeToolCall("tool_call_update", ev.ToolCallID, kind, title, status, ev.Args, ev.Result)
			case mow.EventCompactStart:
				a.emitSessionUpdate(p.SessionID, map[string]any{
					"sessionUpdate": "compact_start",
					"auto":          ev.Auto,
				})
			case mow.EventCompact:
				a.emitSessionUpdate(p.SessionID, map[string]any{
					"sessionUpdate":   "compact",
					"auto":            ev.Auto,
					"layer":           string(ev.Layer),
					"chars_before":    ev.CharsBefore,
					"chars_after":     ev.CharsAfter,
					"chars_saved":     ev.CharsSaved,
					"messages_before": ev.MessagesBefore,
					"messages_after":  ev.MessagesAfter,
					"over_budget":     ev.OverBudget,
				})
			case mow.EventGoalStart, mow.EventGoalStep, mow.EventGoalDone, mow.EventGoalFail, mow.EventGoalPartial, mow.EventGoalBlocked:
				update := map[string]any{"sessionUpdate": "goal", "type": string(ev.Type)}
				if ev.Goal != nil {
					update["goal"] = ev.Goal
				}
				a.emitSessionUpdate(p.SessionID, update)
			case mow.EventRunEnd:
				a.emitUsage(p.SessionID, ev.InputTokens, ev.OutputTokens, ev.CachedInputTokens)
			}
		})
		popt := mow.PromptOpts{Ephemeral: p.Ephemeral}
		if mode == ModeAsk {
			popt.ReadOnly = true
			popt.SystemAppend = "Session mode is ask (read-only): do not use write, edit, or bash. Prefer read/glob/grep and explanations."
		}
		res, err := a.eng.PromptWith(ctx, text, popt)
		unsub()
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
		if !streamed.Load() && res.Text != "" {
			writeAgentText(res.Text)
		}
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"stopReason": acpStopReason(res.StopReason),
				"usage": map[string]int{
					"inputTokens":   res.Usage.InputTokens,
					"outputTokens":  res.Usage.OutputTokens,
					"input_tokens":  res.Usage.InputTokens,
					"output_tokens": res.Usage.OutputTokens,
				},
			},
		})
	case "authenticate":
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "logout":
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "session/close":
		a.handleSessionClose(req)
	case "session/resume":
		a.handleSessionLoad(parent, req, false)
	case "session/list":
		cwd := a.eng.Workspace()
		a.mu.Lock()
		list := make([]map[string]any, 0, len(a.sessions)+4)
		seen := map[string]bool{}
		for sid := range a.sessions {
			list = append(list, map[string]any{"sessionId": sid, "cwd": cwd, "title": sid})
			seen[sid] = true
		}
		a.mu.Unlock()
		if infos, err := a.eng.Sessions(); err == nil {
			for _, in := range infos {
				if seen[in.ID] {
					continue
				}
				row := map[string]any{"sessionId": in.ID, "cwd": cwd, "title": in.ID}
				if !in.Updated.IsZero() {
					row["updatedAt"] = in.Updated.UTC().Format("2006-01-02T15:04:05Z")
				}
				if p := strings.TrimSpace(in.Preview); p != "" {
					row["title"] = p
				}
				list = append(list, row)
				seen[in.ID] = true
			}
		}
		if engSID := a.eng.SessionID(); engSID != "" && !seen[engSID] {
			list = append(list, map[string]any{"sessionId": engSID, "cwd": cwd, "title": engSID})
		}
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"sessions": list}})
	case "session/delete":
		a.handleSessionClose(req)
	case "session/set_mode", "session/setMode":
		var p struct {
			SessionID string `json:"sessionId"`
			ModeID    string `json:"modeId"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "sessionId required"}})
			return
		}
		sid := strings.TrimSpace(p.SessionID)
		if err := a.requireBound(sid); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			return
		}
		if err := a.applyModeConfig(sid, p.ModeID); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			return
		}
		// Keep configOptions mode selector in sync for clients that use it.
		a.mu.Lock()
		mode := ModeCode
		if s := a.sessions[sid]; s != nil {
			mode = s.mode
		}
		a.mu.Unlock()
		opts := a.sessionConfigOptions(parent, mode)
		a.notifyConfigOptions(sid, opts)
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "session/set_config_option", "session/setConfigOption":
		var p struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
			// value is string for select options; boolean for type boolean (unused today).
			Value any    `json:"value"`
			Type  string `json:"type"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "sessionId and configId required"}})
			return
		}
		configID := strings.TrimSpace(p.ConfigID)
		if configID == "" {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "configId required"}})
			return
		}
		sid := strings.TrimSpace(p.SessionID)
		if err := a.requireBound(sid); err != nil {
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
			return
		}
		switch configID {
		case configIDModel:
			val, _ := p.Value.(string)
			if val == "" {
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "model value required"}})
				return
			}
			if err := a.applyModelConfig(parent, val); err != nil {
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
				return
			}
			// Model change may change available efforts; realign to default_effort.
			if efforts := a.eng.Efforts(); len(efforts) > 0 {
				cur := a.eng.Effort()
				ok := cur == ""
				for _, e := range efforts {
					if strings.EqualFold(e, cur) {
						ok = true
						break
					}
				}
				if !ok {
					_ = a.eng.SetEffort(a.eng.DefaultEffort())
				}
			}
		case configIDMode:
			val, _ := p.Value.(string)
			if val == "" {
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "mode value required"}})
				return
			}
			if err := a.applyModeConfig(sid, val); err != nil {
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
				return
			}
		case configIDEffort:
			val, _ := p.Value.(string)
			if strings.EqualFold(strings.TrimSpace(val), "default") {
				val = ""
			}
			if err := a.eng.SetEffort(val); err != nil {
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
				return
			}
		case configIDApprovals:
			val, _ := p.Value.(string)
			if err := a.applyApprovalsConfig(sid, val); err != nil {
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: err.Error()}})
				return
			}
		default:
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "unknown configId: " + configID}})
			return
		}
		a.mu.Lock()
		mode := ModeCode
		if s := a.sessions[sid]; s != nil && s.mode != "" {
			mode = s.mode
		}
		a.mu.Unlock()
		opts := a.sessionConfigOptions(parent, mode)
		if opts == nil {
			opts = []map[string]any{}
		}
		a.notifyConfigOptions(sid, opts)
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"configOptions": opts}})
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
		if a.handleExtra(parent, req) {
			return
		}
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

func (a *agentServer) emitSessionUpdate(sid string, update map[string]any) {
	if sid == "" {
		a.mu.Lock()
		sid = a.activeSID
		a.mu.Unlock()
	}
	if sid == "" || update == nil {
		return
	}
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sid,
			"update":    update,
		}),
	})
}

func (a *agentServer) jailPath(p string) (full string, err error) {
	return resolveInWorkspace(a.eng.Workspace(), p)
}

// resolveInWorkspace joins p to workspace ws, resolves symlinks, and ensures
// the result stays inside ws (same jail as engine tool paths).
func resolveInWorkspace(ws, p string) (string, error) {
	pol := &policy.Policy{Workspace: ws}
	return pol.ResolvePath(p)
}

func (a *agentServer) readWorkspaceFile(p string) (string, error) {
	pol := &policy.Policy{Workspace: a.eng.Workspace()}
	_, data, err := toolspkg.ReadFileJailed(pol, p)
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
	pol := &policy.Policy{Workspace: a.eng.Workspace()}
	_, err := toolspkg.WriteFileJailed(pol, p, data, 0o644)
	return err
}

func nilIfRunning(t *termSession) any {
	if t == nil || !t.closed.Load() {
		return nil
	}
	return int(t.code.Load())
}

func acpStopReason(reason string) string {
	switch reason {
	case mow.StopCancelled:
		return "cancelled"
	case mow.StopMaxTurns, mow.StopStuck, mow.StopBudget, mow.StopTruncated:
		return "max_turn_requests"
	case mow.StopError:
		return "end_turn"
	default:
		return "end_turn"
	}
}

func (a *agentServer) emitUsage(sessionID string, input, output, cached int) {
	if a == nil || (input == 0 && output == 0 && cached == 0) {
		return
	}
	used := map[string]any{
		"inputTokens":  input,
		"outputTokens": output,
	}
	if cached > 0 {
		used["cachedReadTokens"] = cached
	}
	update := map[string]any{
		"sessionUpdate": "usage_update",
		"used":          used,
	}
	if a.eng != nil {
		lim := a.eng.Limits()
		if lim.ContextWindow > 0 {
			update["size"] = map[string]any{"contextWindow": lim.ContextWindow}
		}
	}
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sessionID,
			"update":    update,
		}),
	})
}
