// Package rpc provides a JSON-RPC 2.0 control plane for embedders over
// line-delimited JSON (one object per line).
//
// Requests may include "jsonrpc":"2.0" but need not — minimal clients that
// send only id/method/params still work:
//
//	{"jsonrpc":"2.0","id":1,"method":"prompt","params":{"text":"hello"}}
//	{"id":2,"method":"cancel"}
//	{"id":3,"method":"status"}
//	{"id":4,"method":"session"}
//	{"id":5,"method":"version"}
//	{"id":6,"method":"ping"}
//
// Responses and notifications are conformant (jsonrpc tag; errors carry a
// standard code):
//
//	{"jsonrpc":"2.0","id":1,"result":{"text":"…","session_id":"…","run_id":"…","stop_reason":"completed"}}
//	{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"…","data":{"text":"…","session_id":"…"}}}
//	{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"unknown method …"}}
//
// While a prompt runs, unsolicited event notifications may be written (no id):
//
//	{"jsonrpc":"2.0","method":"event","params":{"type":"loop.token","run_id":"…","delta":"…"}}
//
// Cancel/status are handled concurrently so a host can abort an in-flight prompt.
// Control methods use a dedicated channel so a full prompt queue cannot starve cancel.
//
// Note: Serve uses Engine.AddOnEvent so existing host listeners keep receiving events.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/subosito/mow"
)

// maxLineBytes is the maximum stdin line accepted (scanner buffer).
const maxLineBytes = 1 << 20 // 1 MiB

// maxEventDeltaRunes caps streamed text fields on event notifications so a
// long token stream cannot grow unbounded in the host reader.
const maxEventDeltaRunes = 8 << 10 // 8k runes

// maxPromptTextRunes caps prompt params text accepted over RPC.
const maxPromptTextRunes = 512 << 10 // 512k runes

// Server serves RPC over r/w using a single Engine.
type Server struct {
	Engine *mow.Engine
	In     io.Reader
	Out    io.Writer

	// StreamEvents when true (default) writes method=event notifications during prompt.
	// Set false to only return the final prompt result.
	StreamEvents *bool

	encMu sync.Mutex
}

// jsonRPCVersion tags every response and notification; requests may omit it
// (we stay tolerant of minimal clients) but a conformant JSON-RPC 2.0 client
// works unchanged.
const jsonRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// notification is a server-push line (events during prompt).
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// reply/replyErr/notify stamp the JSON-RPC version so every emitted line is
// conformant (the many call sites never forget it).
func (s *Server) reply(id json.RawMessage, result any) {
	s.write(response{JSONRPC: jsonRPCVersion, ID: id, Result: result})
}

func (s *Server) replyErr(id json.RawMessage, code int, msg string) {
	s.write(response{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *Server) notify(method string, params any) {
	s.write(notification{JSONRPC: jsonRPCVersion, Method: method, Params: params})
}

func isControlMethod(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "cancel", "status", "session", "session_id", "ping", "version":
		return true
	default:
		return false
	}
}

// Serve reads lines until EOF. prompt runs in a worker; cancel/status stay responsive.
func (s *Server) Serve(ctx context.Context) error {
	if s.Engine == nil {
		return fmt.Errorf("rpc: nil engine")
	}
	if s.In == nil {
		s.In = io.NopCloser(strings.NewReader(""))
	}
	if s.Out == nil {
		return fmt.Errorf("rpc: nil out")
	}

	stream := true
	if s.StreamEvents != nil {
		stream = *s.StreamEvents
	}
	if stream {
		unsub := s.Engine.AddOnEvent(func(ev mow.Event) {
			s.notify("event", capEvent(ev))
		})
		defer unsub()
	}

	// Control channel is preferred so cancel is never blocked behind a full
	// prompt queue. Prompt channel has a modest buffer; overflow returns an error.
	controlCh := make(chan request, 16)
	promptCh := make(chan request, 4)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(s.In)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var req request
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				s.replyErr(nil, codeParseError, "invalid json: "+err.Error())
				continue
			}
			if isControlMethod(req.Method) {
				select {
				case controlCh <- req:
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				}
				continue
			}
			select {
			case promptCh <- req:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
				// Do not block the reader: cancel must stay readable.
				s.replyErr(req.ID, codeInternalError, "request queue full; retry after current prompts finish")
			}
		}
		if err := sc.Err(); err != nil {
			errCh <- err
			return
		}
		close(controlCh)
		close(promptCh)
	}()

	var promptWG sync.WaitGroup
	controlOpen, promptOpen := true, true
	for controlOpen || promptOpen {
		// Prefer control when both are ready.
		select {
		case <-ctx.Done():
			s.Engine.Cancel()
			promptWG.Wait()
			return ctx.Err()
		case err := <-errCh:
			s.Engine.Cancel()
			promptWG.Wait()
			return err
		case req, ok := <-controlCh:
			if !ok {
				controlOpen = false
				controlCh = nil
				continue
			}
			s.dispatch(ctx, req, &promptWG)
		default:
			select {
			case <-ctx.Done():
				s.Engine.Cancel()
				promptWG.Wait()
				return ctx.Err()
			case err := <-errCh:
				s.Engine.Cancel()
				promptWG.Wait()
				return err
			case req, ok := <-controlCh:
				if !ok {
					controlOpen = false
					controlCh = nil
					continue
				}
				s.dispatch(ctx, req, &promptWG)
			case req, ok := <-promptCh:
				if !ok {
					promptOpen = false
					promptCh = nil
					continue
				}
				s.dispatch(ctx, req, &promptWG)
			}
		}
	}
	promptWG.Wait()
	return nil
}

func (s *Server) dispatch(ctx context.Context, req request, promptWG *sync.WaitGroup) {
	if strings.TrimSpace(req.Method) == "" {
		s.replyErr(req.ID, codeInvalidRequest, "missing method")
		return
	}
	switch strings.ToLower(req.Method) {
	case "prompt":
		promptWG.Add(1)
		go func(req request) {
			defer promptWG.Done()
			s.handlePrompt(ctx, req)
		}(req)
	case "cancel":
		s.Engine.Cancel()
		s.reply(req.ID, map[string]any{"ok": true})
	case "status":
		s.reply(req.ID, s.Engine.Status())
	case "session", "session_id":
		s.reply(req.ID, map[string]any{
			"session_id": s.Engine.SessionID(),
			"workspace":  s.Engine.Workspace(),
			"model":      s.Engine.Model(),
			"wire":       s.Engine.Wire(),
		})
	case "ping":
		s.reply(req.ID, "pong")
	case "version":
		s.reply(req.ID, map[string]any{
			"name":    "mow",
			"version": mow.VersionString(),
			"rpc":     "2",
			"package": "github.com/subosito/mow",
		})
	default:
		s.replyErr(req.ID, codeMethodNotFound, "unknown method "+req.Method)
	}
}

func (s *Server) handlePrompt(ctx context.Context, req request) {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if utf8.RuneCountInString(p.Text) > maxPromptTextRunes {
		s.replyErr(req.ID, codeInvalidRequest, fmt.Sprintf("prompt text exceeds %d runes", maxPromptTextRunes))
		return
	}

	res, err := s.Engine.Prompt(ctx, p.Text)
	if err != nil {
		s.write(response{JSONRPC: jsonRPCVersion, ID: req.ID,
			Error: &rpcError{
				Code:    codeInternalError,
				Message: err.Error(),
				Data: map[string]any{
					"text": res.Text, "session_id": res.SessionID,
					"run_id": res.RunID, "stop_reason": res.StopReason,
				},
			},
		})
		return
	}
	s.reply(req.ID, map[string]any{
		"text": res.Text, "session_id": res.SessionID, "run_id": res.RunID, "stop_reason": res.StopReason,
	})
}

// capEvent trims large string fields on streamed events for host safety.
func capEvent(ev mow.Event) mow.Event {
	if utf8.RuneCountInString(ev.Delta) > maxEventDeltaRunes {
		ev.Delta = trimRunes(ev.Delta, maxEventDeltaRunes) + "…"
	}
	if utf8.RuneCountInString(ev.Error) > maxEventDeltaRunes {
		ev.Error = trimRunes(ev.Error, maxEventDeltaRunes) + "…"
	}
	return ev
}

func trimRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

func (s *Server) write(v any) {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	enc := json.NewEncoder(s.Out)
	_ = enc.Encode(v)
}
