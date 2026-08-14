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
// Beyond the core methods, a UI process (an external TUI that does not embed
// Engine) can drive the whole session over the same pipe: sessions, transcript,
// steer, slash.list, slash, and a permission gate (perm.set / perm.decide with
// perm.ask notifications). The gate is fail-open until a UI opts into ask mode,
// so existing headless scripts are unaffected. See README.md for the table.
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
	"github.com/subosito/mow/ext"
)

// maxLineBytes is the maximum stdin line accepted (scanner buffer).
const maxLineBytes = 1 << 20 // 1 MiB

// maxEventDeltaRunes caps streamed text fields on event notifications so a
// long token stream cannot grow unbounded in the host reader.
const maxEventDeltaRunes = 8 << 10  // 8k runes
const maxToolResultRunes = 64 << 10 // live write/edit diff cards

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

	// Permission gate state (see perm.go). Zero value is fail-open: until a
	// UI sends perm.set {"mode":"ask"}, every tool call is allowed, so
	// headless scripts behave exactly as before.
	permMu      sync.Mutex
	askMode     bool
	alwaysAllow map[string]bool
	pending     map[string]pendingPerm
	permSeq     int64
}

// jsonRPCVersion tags every response and notification; requests may omit it
// (we stay tolerant of minimal clients) but a conformant JSON-RPC 2.0 client
// works unchanged.
const jsonRPCVersion = "2.0"

// rpcProtocolVersion is mow's method-surface compatibility epoch, distinct
// from JSON-RPC 2.0 and the mow release version. It changes only for a breaking
// wire change; additive methods are discovered through capabilities.
//
// Version 1 is the first public contract for external hosts. Pre-release values
// 2–4 were never published compatibility epochs.
const rpcProtocolVersion = "1"

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

	// parseErr, when set, marks a line that failed to decode. It travels the
	// same channel as real requests so its error reply lands in arrival order
	// instead of jumping ahead of replies to earlier lines.
	parseErr *rpcError

	// notification is true when the request carried no "id" member at all.
	// JSON-RPC 2.0 §4.1: a server MUST NOT reply to a notification. Note this
	// is distinct from an explicit "id":null, which is a (malformed but
	// answerable) request — hence a separate flag rather than len(ID) == 0.
	notification bool
}

// UnmarshalJSON records whether "id" was present so notifications can be
// answered with silence instead of a stray response line.
func (r *request) UnmarshalJSON(b []byte) error {
	type raw request
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	var out raw
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	_, hasID := probe["id"]
	out.notification = !hasID
	*r = request(out)
	return nil
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

// replyTo/replyErrTo are the request-aware forms: silent for notifications
// (JSON-RPC 2.0 §4.1), otherwise identical to reply/replyErr.
func (s *Server) replyTo(req request, result any) {
	if req.notification {
		return
	}
	s.reply(req.ID, result)
}

func (s *Server) replyErrTo(req request, code int, msg string) {
	if req.notification {
		return
	}
	s.replyErr(req.ID, code, msg)
}

func (s *Server) notify(method string, params any) {
	s.write(notification{JSONRPC: jsonRPCVersion, Method: method, Params: params})
}

// streamEvents reports whether event notifications are pushed during prompt
// (default true; StreamEvents=false makes prompt return only its final result).
func (s *Server) streamEvents() bool {
	if s.StreamEvents == nil {
		return true
	}
	return *s.StreamEvents
}

// methodNames is the served method surface, in dispatch order. It is the
// source of truth for capability discovery: a client reads this from
// `version` (or `capabilities`) and feature-detects up front instead of
// probing and handling -32601. Keep it in sync when adding a case to dispatch
// — TestRPCCapabilitiesMatchDispatch fails otherwise.
var methodNames = []string{
	"prompt", "cancel", "status", "session", "sessions", "transcript", "steer",
	"slash", "slash.list",
	"perm.set", "perm.decide",
	"model.list", "model.set",
	"effort.list", "effort.set",
	"context", "compact", "rewind",
	"skill.list", "skill.activate",
	"ping", "version", "capabilities", "extension.config",
}

// capabilitiesResult describes what this build can do. Booleans are for
// behavior a client cannot infer from a method name alone.
func (s *Server) capabilitiesResult() map[string]any {
	methods := append([]string{}, methodNames...)
	control := make([]string, 0, len(methods))
	for _, m := range methods {
		if isControlMethod(m) {
			control = append(control, m)
		}
	}
	out := map[string]any{
		"rpc":     rpcProtocolVersion,
		"methods": methods,
		// Control methods are answered while a prompt is in flight.
		"control_methods": control,
		"features": map[string]any{
			"streaming_events": s.streamEvents(),
			"ephemeral_prompt": true,
			"prompt_file_refs": true,
			"permission_gate":  true,
			"extra_roots":      true,
			"batch":            false,
			"notifications":    true,
		},
	}
	if features := ext.OptionalFeatures(); len(features) > 0 {
		optional := make([]map[string]any, 0, len(features))
		for _, feature := range features {
			row := map[string]any{"id": feature.ID, "linked": true}
			if len(feature.Events) > 0 {
				row["events"] = append([]string(nil), feature.Events...)
			}
			optional = append(optional, row)
		}
		out["optional"] = map[string]any{"features": optional}
	}
	return out
}

func isControlMethod(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "cancel", "status", "session", "session_id", "ping", "version",
		"sessions", "transcript", "steer",
		"slash", "slash.list",
		"perm.set", "perm.decide", "model.list", "model.set",
		"effort.list", "effort.set",
		"context", "rewind", "skill.list", "skill.activate",
		"capabilities", "extension.config":
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

	if s.streamEvents() {
		unsub := s.Engine.AddOnEvent(func(ev mow.Event) {
			s.notify("event", capEvent(ev))
		})
		defer unsub()
	}

	// The permission gate is installed once, always: it is inert (allow) until
	// a UI switches the server into ask mode with perm.set.
	unsubPre := s.Engine.AddPreTool(s.preTool)
	defer unsubPre()

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
				// A JSON array is a batch: well-formed JSON, but unsupported
				// here (a stdio control plane has no round trip to amortize),
				// so it is an invalid request rather than a parse error.
				code, msg := codeParseError, "invalid json: "+err.Error()
				if strings.HasPrefix(line, "[") {
					code, msg = codeInvalidRequest, "batch requests are not supported; send one object per line"
				}
				// Queue it rather than writing here: the reader runs ahead of
				// the dispatch loop, so a direct write would print this error
				// before the replies to lines that arrived earlier.
				req = request{parseErr: &rpcError{Code: code, Message: msg}}
			}
			if req.parseErr != nil || isControlMethod(req.Method) {
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
				s.replyErrTo(req, codeInternalError, "request queue full; retry after current prompts finish")
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
	if req.parseErr != nil {
		// Undecodable line: no id to echo, so reply with a null id (§5).
		s.write(response{JSONRPC: jsonRPCVersion, Error: req.parseErr})
		return
	}
	if strings.TrimSpace(req.Method) == "" {
		s.replyErrTo(req, codeInvalidRequest, "missing method")
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
		s.replyTo(req, map[string]any{"ok": true})
	case "status":
		s.replyTo(req, s.statusResult())
	case "sessions":
		s.handleSessions(req)
	case "transcript":
		s.handleTranscript(req)
	case "steer":
		s.handleSteer(req)
	case "slash.list":
		s.handleSlashList(req)
	case "slash":
		// A slash command may run for a while (it can drive the engine), so
		// serve it off the loop like prompt — cancel stays responsive.
		promptWG.Add(1)
		go func(req request) {
			defer promptWG.Done()
			s.handleSlash(ctx, req)
		}(req)
	case "perm.set":
		s.handlePermSet(req)
	case "perm.decide":
		s.handlePermDecide(req)
	case "model.list":
		s.handleModelList(ctx, req)
	case "model.set":
		s.handleModelSet(req)
	case "effort.list":
		s.handleEffortList(req)
	case "effort.set":
		s.handleEffortSet(req)
	case "context":
		s.handleContext(req)
	case "compact":
		s.handleCompact(req)
	case "rewind":
		s.handleRewind(req)
	case "skill.list":
		s.handleSkillList(req)
	case "skill.activate":
		s.handleSkillActivate(req)
	case "session", "session_id":
		out := map[string]any{
			"session_id": s.Engine.SessionID(),
			"workspace":  s.Engine.Workspace(),
			"model":      s.Engine.Model(),
			"wire":       s.Engine.Wire(),
		}
		addExtraRootMetadata(out, s.Engine)
		s.replyTo(req, out)
	case "capabilities":
		s.replyTo(req, s.capabilitiesResult())
	case "extension.config":
		s.handleExtensionConfig(req)
	case "ping":
		s.replyTo(req, "pong")
	case "version":
		out := s.capabilitiesResult()
		out["name"] = "mow"
		out["version"] = mow.VersionString()
		out["package"] = "github.com/subosito/mow"
		s.replyTo(req, out)
	default:
		s.replyErrTo(req, codeMethodNotFound, "unknown method "+req.Method)
	}
}

// mowiConfig is intentionally a presentation-only allowlist. The RPC does not
// expose arbitrary extension sections, where operators may keep credentials.
type mowiConfig struct {
	PermissionMode string `yaml:"permission_mode" json:"permission_mode,omitempty"`
	Theme          string `yaml:"theme" json:"theme,omitempty"`
	Welcome        *bool  `yaml:"welcome" json:"welcome,omitempty"`
	WelcomeMessage string `yaml:"welcome_message" json:"welcome_message,omitempty"`
	Prompt         string `yaml:"prompt" json:"prompt,omitempty"`
}

func (s *Server) handleExtensionConfig(req request) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErrTo(req, codeInvalidRequest, "invalid extension.config params")
		return
	}
	if strings.TrimSpace(p.Name) != "mowi" {
		s.replyErrTo(req, codeInvalidRequest, "only extension mowi is exposed")
		return
	}
	var out mowiConfig
	if err := s.Engine.Extension("mowi", &out); err != nil {
		s.replyErrTo(req, codeInternalError, "decode extensions.mowi: "+err.Error())
		return
	}
	s.replyTo(req, out)
}

func (s *Server) handlePrompt(ctx context.Context, req request) {
	var p struct {
		Text      string `json:"text"`
		Ephemeral bool   `json:"ephemeral"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if utf8.RuneCountInString(p.Text) > maxPromptTextRunes {
		s.replyErrTo(req, codeInvalidRequest, fmt.Sprintf("prompt text exceeds %d runes", maxPromptTextRunes))
		return
	}

	text, attached := s.expandPromptFileRefs(p.Text)
	res, err := s.Engine.PromptWith(ctx, text, mow.PromptOpts{Ephemeral: p.Ephemeral})
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
	s.replyTo(req, map[string]any{
		"text": res.Text, "session_id": res.SessionID, "run_id": res.RunID, "stop_reason": res.StopReason,
		"usage": map[string]any{
			"input_tokens":  res.Usage.InputTokens,
			"output_tokens": res.Usage.OutputTokens,
		},
		"ephemeral": p.Ephemeral,
		"attached":  attached,
	})
}

// capEvent trims large string fields on streamed events for host safety.
func capEvent(ev mow.Event) mow.Event {
	// Tool results carry write/edit diffs. Keep enough of those results for an
	// external TUI to paint a useful live review card; ordinary event fields
	// remain under the smaller delta cap.
	if utf8.RuneCountInString(ev.Result) > maxToolResultRunes {
		ev.Result = trimRunes(ev.Result, maxToolResultRunes) + "…"
	}
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
