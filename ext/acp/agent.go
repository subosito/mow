package acp

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subosito/mow"
	"github.com/subosito/mow/internal/policy"
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
	name := cmp.Or(opt.Name, "mow")
	ver := cmp.Or(opt.Version, mow.Version)

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
	eng       *mow.Engine
	out       io.Writer
	name      string
	ver       string
	mu        sync.Mutex
	encMu     sync.Mutex
	writeErr  error                   // sticky output failure, guarded by encMu
	stopWrite context.CancelCauseFunc // cancels serving on output failure
	cancels   map[string]context.CancelFunc
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
	// wg tracks handler goroutines; serve waits for them after stdin ends.
	wg sync.WaitGroup
}

// acpSession is ACP mode/approvals for the bound session.
type acpSession struct {
	mode      string // ModeAsk | ModeCode
	approvals string // ApprovalPrompt | ApprovalAlways
}

// maxLineBytes caps one stdin JSON-RPC line (base64 media rides in prompts).
const maxLineBytes = 8 << 20

// stdinLine is one line from the editor, or the read error that ended stdin.
type stdinLine struct {
	text    string
	tooLong bool // line exceeded maxLineBytes and was discarded
	err     error
}

// readLines feeds stdin lines to out until EOF, a read error, or ctx is done.
// An over-long line is reported (and dropped) instead of ending the stream.
func readLines(ctx context.Context, in io.Reader, out chan<- stdinLine) {
	defer close(out)
	send := func(l stdinLine) bool {
		select {
		case out <- l:
			return true
		case <-ctx.Done():
			return false
		}
	}
	br := bufio.NewReaderSize(in, 64*1024)
	for {
		var line []byte
		tooLong := false
		for {
			chunk, err := br.ReadSlice('\n')
			if !tooLong {
				line = append(line, chunk...)
				if len(line) > maxLineBytes {
					tooLong, line = true, nil
				}
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				// EOF (possibly after a partial last line) or a read error.
				if (tooLong || len(line) > 0) && !send(stdinLine{text: string(line), tooLong: tooLong}) {
					return
				}
				if err != io.EOF {
					send(stdinLine{err: err})
				}
				return
			}
			break
		}
		if !send(stdinLine{text: string(line), tooLong: tooLong}) {
			return
		}
	}
}

func (a *agentServer) serve(ctx context.Context, in io.Reader) error {
	a.sessions = map[string]*acpSession{}
	// Every handler runs under this ctx so stdin EOF / SIGINT cancels in-flight
	// prompts before wg.Wait (defers run LIFO): no turn keeps running against a
	// closed editor, and no late write hits a closed stdout.
	ctx, cancel := context.WithCancelCause(ctx)
	a.encMu.Lock()
	a.stopWrite = cancel
	a.encMu.Unlock()
	defer a.wg.Wait()
	defer cancel(nil)
	lines := make(chan stdinLine)
	go readLines(ctx, in, lines)
	// Catalog-backed mutations are ordered but never run on the input loop:
	// permission replies, cancellation, and ping must remain responsive.
	configRequests := make(chan request, 8)
	a.wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-configRequests:
				if ctx.Err() != nil {
					return
				}
				a.handleRequest(ctx, req)
			}
		}
	})
	for {
		var ln stdinLine
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case l, ok := <-lines:
			if !ok {
				return context.Cause(ctx)
			}
			ln = l
		}
		if ln.err != nil {
			return ln.err
		}
		if ln.tooLong {
			a.write(response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: errInvalid, Message: fmt.Sprintf("line exceeds %d bytes", maxLineBytes)},
			})
			continue
		}
		line := strings.TrimSpace(ln.text)
		if line == "" {
			continue
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
		case "session/set_config_option", "session/setConfigOption", "session/set_mode", "session/setMode":
			select {
			case configRequests <- req:
			default:
				a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "configuration request queue full; wait for the pending change"}})
			}
		case "session/prompt", "session/load", "terminal/wait_for_exit", "terminal/waitForExit", "compact", "slash", "rewind", "skill.activate", "transcript", "context":
			// Long-blocking methods run off the read loop so session/cancel
			// (and other traffic) is still read while they are in flight.
			// compact rewrites in-memory history and can take long enough that
			// a blocked stdin read looks like a dead agent to the TUI.
			a.wg.Add(1)
			go func(req request) {
				defer a.wg.Done()
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
}

func (a *agentServer) handleNotification(n notification) {
	if n.Method == "session/cancel" {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(n.Params, &p)
		a.mu.Lock()
		if cancel, ok := a.cancels[p.SessionID]; ok && cancel != nil {
			// This is the only normal path that interrupts an active model HTTP
			// stream mid-turn. Emit the session and source before cancellation so
			// a gateway 499 can be correlated to an explicit ACP client action.
			slog.Info("cancel active prompt", "session", p.SessionID, "cause", "acp_session_cancel")
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
						// ACP standard media blocks: image/audio. Video is accepted as a
						// mow extension but is not advertised as a standard capability.
						"image": true, "audio": true, "embeddedContext": true,
					},
					"mcpCapabilities": map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{
						"list":   map[string]any{},
						"delete": map[string]any{},
						"close":  map[string]any{},
						"resume": map[string]any{},
					},
					"auth": map[string]any{},
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
		text, media, err := materializePrompt(p.Prompt, ws, p.SessionID)
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
		if a.cancels[p.SessionID] != nil {
			// A second prompt would clobber cancels/activeSID/SetOnToken.
			a.mu.Unlock()
			cancel()
			a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "prompt already in flight for session"}})
			return
		}
		a.cancels[p.SessionID] = cancel
		a.activeSID = p.SessionID
		mode := ModeCode
		if s := a.sessions[p.SessionID]; s != nil && s.mode != "" {
			mode = s.mode
		}
		a.mu.Unlock()
		var cleanupOnce sync.Once
		cleanup := func() {
			cleanupOnce.Do(func() {
				cancel()
				a.mu.Lock()
				delete(a.cancels, p.SessionID)
				if a.activeSID == p.SessionID {
					a.activeSID = ""
				}
				a.mu.Unlock()
			})
		}
		defer cleanup()

		if body, exclusiveBusy, ok := a.trySlash(ctx, p.SessionID, text); ok {
			// Release the per-session in-flight slot before publishing the final
			// response. ACP clients may issue the next prompt immediately after
			// reading it; deferring cleanup until this goroutine returns races that
			// next request and spuriously reports "prompt already in flight".
			cleanup()
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

		// Stream main-model tokens, tool calls, and delegate/goal activity to
		// the client. Exclusive slash commands (/goal) share this forwarder so
		// a long goal streams the same live progress as a plain prompt.
		fwd := a.startLiveForward(p.SessionID)
		writeAgentText := fwd.writeText
		streamed := &fwd.streamed
		unsub := fwd.stop
		popt := mow.PromptOpts{Ephemeral: p.Ephemeral, AskUser: a.askUser, UserParts: media}
		if mode == ModeAsk {
			popt.SystemAppend = "Session mode is ask: confirm with the operator before write, edit, or bash."
		}
		res, err := a.eng.PromptWith(ctx, text, popt)
		unsub()
		// Release the slot before any terminal response lets a client start
		// another prompt. Save cancellation first: cleanup cancels ctx too.
		cancelled := ctx.Err() != nil
		cleanup()
		// A stopped investigation can still carry a useful final answer. Send
		// it before the terminal error so non-streaming hosts retain it too.
		if err != nil && !cancelled && !streamed.Load() && res.Text != "" {
			writeAgentText(res.Text)
		}
		if err != nil {
			if cancelled {
				a.write(response{
					JSONRPC: "2.0", ID: req.ID,
					Result: map[string]any{"stopReason": "cancelled"},
				})
				return
			}
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: errInternal, Message: promptErrorMessage(err)},
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
		// Mode is applied; the catalog round trip for configOptions runs off
		// the read loop so a slow gateway does not stall session/cancel.
		a.wg.Go(func() {
			opts := a.sessionConfigOptions(parent, mode)
			a.notifyConfigOptions(sid, opts)
			a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		})
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
		// Option is applied; the catalog round trip runs off the read loop.
		a.wg.Go(func() {
			opts := a.sessionConfigOptions(parent, mode)
			if opts == nil {
				opts = []map[string]any{}
			}
			a.notifyConfigOptions(sid, opts)
			a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"configOptions": opts}})
		})
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

func (a *agentServer) write(v any) error {
	a.encMu.Lock()
	defer a.encMu.Unlock()
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.out == nil {
		return nil
	}
	if err := json.NewEncoder(a.out).Encode(v); err != nil {
		a.writeErr = fmt.Errorf("acp output: %w", err)
		if a.stopWrite != nil {
			a.stopWrite(a.writeErr)
		}
		return a.writeErr
	}
	return nil
}

// toolCallAttachesResult is false for read/glob/grep: those bodies are
// session history only. write/edit (and bash) still go on the wire for cards.
func toolCallAttachesResult(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read", "glob", "grep":
		return false
	default:
		return true
	}
}

func modelStatusUpdate(kind string, ev mow.Event) map[string]any {
	u := map[string]any{"sessionUpdate": kind}
	if ev.Delta != "" {
		u["delta"] = ev.Delta
	}
	if ev.Model != "" {
		u["model"] = ev.Model
	}
	return u
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

// resolveInWorkspace joins p to workspace ws, resolves symlinks, and ensures
// the result stays inside ws (same jail as engine tool paths).
func resolveInWorkspace(ws, p string) (string, error) {
	pol := &policy.Policy{Workspace: ws}
	return pol.ResolvePath(p)
}

func (a *agentServer) readWorkspaceFile(p string) (string, error) {
	_, data, err := a.eng.ReadFile(p)
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
	// Engine policy, not a bare workspace jail: ExtraRoots / ReadOnly /
	// WritableRoots apply exactly as they do for the write tool.
	_, err := a.eng.WriteFile(p, data)
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

// emitUsage reports context occupancy and billing for one session.
//
// used is occupancy: the tokens the next prompt already occupies (Engine.ContextTokens,
// which is the last model call's input, or the post-compaction estimate). It is
// NOT the run sum — summing every call pegs the client's used/window chip.
// billed is that run sum (input/output/cachedRead), for hosts that show spend
// separately from the context chip. Both are omitted when zero so a quiet
// turn does not invent a gauge.
func (a *agentServer) emitUsage(sessionID string, occupancy, billedIn, billedOut, billedCached int) {
	if a == nil || (occupancy == 0 && billedIn == 0 && billedOut == 0 && billedCached == 0) {
		return
	}
	update := map[string]any{
		"sessionUpdate": "usage_update",
	}
	if occupancy > 0 {
		if a.eng != nil {
			if lim := a.eng.Limits(); lim.ContextWindow > 0 && occupancy > lim.ContextWindow {
				occupancy = lim.ContextWindow
			}
		}
		update["used"] = map[string]any{"inputTokens": occupancy}
	}
	if billedIn > 0 || billedOut > 0 || billedCached > 0 {
		billed := map[string]any{
			"inputTokens":  billedIn,
			"outputTokens": billedOut,
		}
		if billedCached > 0 {
			billed["cachedReadTokens"] = billedCached
		}
		update["billed"] = billed
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
