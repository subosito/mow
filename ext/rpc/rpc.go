// Package rpc provides a line-delimited JSON control plane for embedders.
//
// Request (one JSON object per line):
//
//	{"id":1,"method":"prompt","params":{"text":"hello"}}
//	{"id":2,"method":"cancel"}
//	{"id":3,"method":"status"}
//	{"id":4,"method":"session"}
//	{"id":5,"method":"version"}
//	{"id":6,"method":"ping"}
//
// Response:
//
//	{"id":1,"result":{"text":"…","session_id":"…","run_id":"…","stop_reason":"completed"}}
//	{"id":1,"error":{"message":"…"}}
//
// While a prompt runs, unsolicited event notifications may be written (no id):
//
//	{"method":"event","params":{"type":"token","run_id":"…","delta":"…"}}
//
// Cancel/status are handled concurrently so a host can abort an in-flight prompt.
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

	"github.com/subosito/mow"
)

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

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// notification is a server-push line (events during prompt).
type notification struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcError struct {
	Message string `json:"message"`
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
			s.write(notification{Method: "event", Params: ev})
		})
		defer unsub()
	}

	reqCh := make(chan request, 8)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(s.In)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var req request
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				s.write(response{Error: &rpcError{Message: "invalid json: " + err.Error()}})
				continue
			}
			select {
			case reqCh <- req:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		}
		if err := sc.Err(); err != nil {
			errCh <- err
			return
		}
		close(reqCh)
	}()

	var promptWG sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			s.Engine.Cancel()
			promptWG.Wait()
			return ctx.Err()
		case err := <-errCh:
			s.Engine.Cancel()
			promptWG.Wait()
			return err
		case req, ok := <-reqCh:
			if !ok {
				promptWG.Wait()
				return nil
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
				s.write(response{ID: req.ID, Result: map[string]any{"ok": true}})
			case "status":
				s.write(response{ID: req.ID, Result: s.Engine.Status()})
			case "session", "session_id":
				s.write(response{ID: req.ID, Result: map[string]any{
					"session_id": s.Engine.SessionID(),
					"workspace":  s.Engine.Workspace(),
					"model":      s.Engine.Model(),
					"wire":       s.Engine.Wire(),
				}})
			case "ping":
				s.write(response{ID: req.ID, Result: "pong"})
			case "version":
				s.write(response{ID: req.ID, Result: map[string]any{
					"name":    "mow",
					"version": mow.VersionString(),
					"rpc":     "2",
					"package": "github.com/subosito/mow",
				}})
			default:
				s.write(response{ID: req.ID, Error: &rpcError{Message: "unknown method " + req.Method}})
			}
		}
	}
}

func (s *Server) handlePrompt(ctx context.Context, req request) {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(req.Params, &p)

	res, err := s.Engine.Prompt(ctx, p.Text)
	if err != nil {
		s.write(response{ID: req.ID, Error: &rpcError{Message: err.Error()}, Result: map[string]any{
			"text": res.Text, "session_id": res.SessionID, "run_id": res.RunID, "stop_reason": res.StopReason,
		}})
		return
	}
	s.write(response{ID: req.ID, Result: map[string]any{
		"text": res.Text, "session_id": res.SessionID, "run_id": res.RunID, "stop_reason": res.StopReason,
	}})
}

func (s *Server) write(v any) {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	enc := json.NewEncoder(s.Out)
	_ = enc.Encode(v)
}
