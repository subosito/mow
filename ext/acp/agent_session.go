package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (a *agentServer) requireBound(sid string) error {
	sid = strings.TrimSpace(sid)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.boundSID == "" {
		return fmt.Errorf("no active session; session/new first")
	}
	if sid != "" && sid != a.boundSID {
		return fmt.Errorf("session %s is not active", sid)
	}
	return nil
}

func (a *agentServer) handleSessionNew(parent context.Context, req request) {
	a.mu.Lock()
	bound := a.boundSID
	fresh := a.freshAfterClose
	a.mu.Unlock()
	if bound != "" {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errInvalid, Message: "session already active; session/close first"},
		})
		return
	}
	if fresh {
		if _, err := a.eng.BeginSession(); err != nil {
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: errInternal, Message: err.Error()},
			})
			return
		}
	}
	sid := strings.TrimSpace(a.eng.SessionID())
	if sid == "" {
		sid = fmt.Sprintf("mow-%d", a.sessionSeq.Add(1))
	}
	mode := a.bind(sid)
	a.writeSessionOpen(parent, req, sid, mode)
}

func (a *agentServer) handleSessionLoad(parent context.Context, req request, replay bool) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
		a.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: errInvalid, Message: "sessionId required"}})
		return
	}
	sid := strings.TrimSpace(p.SessionID)
	a.mu.Lock()
	bound := a.boundSID
	a.mu.Unlock()
	if bound != "" && bound != sid {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errInvalid, Message: "session already active; session/close first"},
		})
		return
	}
	if bound == sid {
		mode := a.bind(sid)
		a.writeSessionOpen(parent, req, sid, mode)
		return
	}
	if engSID := strings.TrimSpace(a.eng.SessionID()); engSID != "" && engSID != sid {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{
				Code:    errInvalid,
				Message: "this process holds session " + engSID + "; restart mow acp --session " + sid,
			},
		})
		return
	}
	mode := a.bind(sid)
	if replay {
		for _, m := range a.eng.Transcript() {
			kind := "user_message_chunk"
			switch strings.ToLower(strings.TrimSpace(m.Role)) {
			case "assistant":
				kind = "agent_message_chunk"
			case "system":
				continue
			}
			a.write(notification{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustJSON(map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": kind,
						"content":       map[string]any{"type": "text", "text": m.Content},
					},
				}),
			})
		}
	}
	a.writeSessionOpen(parent, req, sid, mode)
}

func (a *agentServer) handleSessionClose(req request) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(req.Params, &p)
	sid := strings.TrimSpace(p.SessionID)
	a.mu.Lock()
	if sid == "" {
		sid = a.boundSID
	}
	if a.boundSID != "" && sid != a.boundSID {
		a.mu.Unlock()
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errInvalid, Message: "session " + sid + " is not active"},
		})
		return
	}
	if cancel, ok := a.cancels[sid]; ok && cancel != nil {
		cancel()
		delete(a.cancels, sid)
	}
	delete(a.sessions, sid)
	if a.boundSID == sid {
		a.boundSID = ""
		a.freshAfterClose = true
	}
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
	// Do not block the RPC on peer SIGKILL (cursor-agent ignores TERM).
	go dropSharedPeers()
	a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
}

func (a *agentServer) bind(sid string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions == nil {
		a.sessions = map[string]*acpSession{}
	}
	if a.sessions[sid] == nil {
		a.sessions[sid] = &acpSession{mode: ModeCode, approvals: ApprovalPrompt}
	}
	a.boundSID = sid
	a.freshAfterClose = false
	return a.sessions[sid].mode
}

func (a *agentServer) writeSessionOpen(parent context.Context, req request, sid, mode string) {
	result := map[string]any{
		"sessionId": sid,
		"modes":     modeState(mode),
	}
	if opts := a.sessionConfigOptions(parent, mode); len(opts) > 0 {
		result["configOptions"] = opts
	}
	a.write(response{JSONRPC: "2.0", ID: req.ID, Result: result})
	a.advertiseCommands(sid)
}
