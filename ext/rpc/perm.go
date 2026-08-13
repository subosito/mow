package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/subosito/mow"
)

// askTools are the power tools worth a confirmation prompt. Read-only tools
// (read, glob, grep) never ask: gating them would make ask mode unusable
// without buying any safety the path jail does not already give.
func isAskTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write", "edit", "bash":
		return true
	default:
		return false
	}
}

// pendingPerm is one outstanding perm.ask awaiting a UI decision.
type pendingPerm struct {
	name string
	ch   chan string
}

// AskMode reports whether the server currently asks the UI before power tools.
func (s *Server) AskMode() bool {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	return s.askMode
}

func (s *Server) pendingCount() int {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	return len(s.pending)
}

// preTool is installed once in Serve. Default (no perm.set) is allow, so
// embedders and existing scripts see the pre-v3 behavior.
func (s *Server) preTool(ctx context.Context, e mow.PreToolEvent) (mow.PreToolDecision, error) {
	s.permMu.Lock()
	if !s.askMode || !isAskTool(e.Name) || s.alwaysAllow[strings.ToLower(e.Name)] {
		s.permMu.Unlock()
		return mow.PreToolDecision{}, nil
	}
	id := fmt.Sprintf("perm-%d", atomic.AddInt64(&s.permSeq, 1))
	ch := make(chan string, 1)
	if s.pending == nil {
		s.pending = map[string]pendingPerm{}
	}
	s.pending[id] = pendingPerm{name: strings.ToLower(strings.TrimSpace(e.Name)), ch: ch}
	s.permMu.Unlock()

	defer func() {
		s.permMu.Lock()
		delete(s.pending, id)
		s.permMu.Unlock()
	}()

	args := e.Args
	if len(args) == 0 {
		args = json.RawMessage("null")
	}
	s.notify("perm.ask", map[string]any{
		"id":           id,
		"name":         e.Name,
		"args":         args,
		"tool_call_id": e.ToolCallID,
	})

	select {
	case <-ctx.Done():
		// Cancelled turn: abort the run rather than fabricate a tool result.
		return mow.PreToolDecision{}, ctx.Err()
	case d := <-ch:
		switch d {
		case "allow", "always":
			return mow.PreToolDecision{}, nil
		default:
			return mow.PreToolDecision{Deny: true, Message: "denied by user"}, nil
		}
	}
}

func (s *Server) handlePermSet(req request) {
	var p struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(req.Params, &p)
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	switch mode {
	case "ask", "auto":
	default:
		s.replyErrTo(req, codeInvalidRequest, `perm.set requires params.mode "ask" or "auto"`)
		return
	}
	s.permMu.Lock()
	s.askMode = mode == "ask"
	s.permMu.Unlock()
	s.replyTo(req, map[string]any{"ok": true, "ask_mode": mode == "ask"})
}

func (s *Server) handlePermDecide(req request) {
	var p struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(req.Params, &p)
	decision := strings.ToLower(strings.TrimSpace(p.Decision))
	switch decision {
	case "allow", "deny", "always":
	default:
		s.replyErrTo(req, codeInvalidRequest, `perm.decide requires params.decision "allow", "deny" or "always"`)
		return
	}
	s.permMu.Lock()
	p2, ok := s.pending[strings.TrimSpace(p.ID)]
	if ok && decision == "always" && p2.name != "" {
		if s.alwaysAllow == nil {
			s.alwaysAllow = map[string]bool{}
		}
		s.alwaysAllow[p2.name] = true
	}
	s.permMu.Unlock()
	if !ok {
		s.replyErrTo(req, codeInvalidRequest, "unknown permission id "+p.ID)
		return
	}
	select {
	case p2.ch <- decision:
	default:
	}
	s.replyTo(req, map[string]any{"ok": true})
}
