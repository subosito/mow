package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/subosito/mow"
)

// permissionOptionIDs are the ACP v1 option ids we advertise. Clients must
// echo one of these in RequestPermissionResponse.outcome.optionId.
const (
	optAllowOnce    = "allow_once"
	optAllowAlways  = "allow_always"
	optRejectOnce   = "reject_once"
	optRejectAlways = "reject_always"
)

func isAskTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write", "edit", "bash", "proc_start", "proc_stop":
		return true
	default:
		return false
	}
}

func permissionOptions() []map[string]any {
	return []map[string]any{
		{"optionId": optAllowOnce, "name": "Allow once", "kind": "allow_once"},
		{"optionId": optAllowAlways, "name": "Allow always for this session", "kind": "allow_always"},
		{"optionId": optRejectOnce, "name": "Reject", "kind": "reject_once"},
		{"optionId": optRejectAlways, "name": "Reject always for this session", "kind": "reject_always"},
	}
}

// preTool asks the editor via session/request_permission before power tools.
// Read tools never ask. A client that does not implement the method (JSON-RPC
// -32601) is treated as allow so a partial editor still works. Cancelled
// turns abort the run. Always-allow / always-reject are remembered per tool
// for the rest of this agent process.
func (a *agentServer) preTool(ctx context.Context, e mow.PreToolEvent) (mow.PreToolDecision, error) {
	if a == nil || !isAskTool(e.Name) {
		return mow.PreToolDecision{}, nil
	}
	name := strings.ToLower(strings.TrimSpace(e.Name))

	if a.sessionApprovals() == ApprovalAlways {
		return mow.PreToolDecision{}, nil
	}

	a.mu.Lock()
	if a.alwaysAllow[name] {
		a.mu.Unlock()
		return mow.PreToolDecision{}, nil
	}
	if a.alwaysReject[name] {
		a.mu.Unlock()
		return mow.PreToolDecision{Deny: true, Message: "denied by remembered rule"}, nil
	}
	sid := a.activeSID
	if sid == "" {
		sid = a.eng.SessionID()
	}
	a.mu.Unlock()

	args := e.Args
	if len(args) == 0 {
		args = json.RawMessage("null")
	}
	kind, title := toolCallKindTitle(e.Name, args)
	if title == "" {
		title = e.Name
	} else {
		title = strings.TrimSpace(e.Name + " " + title)
	}

	raw, err := a.callClient(ctx, "session/request_permission", map[string]any{
		"sessionId": sid,
		"toolCall": map[string]any{
			"toolCallId": e.ToolCallID,
			"title":      title,
			"kind":       kind,
			"status":     "pending",
			"rawInput":   args,
		},
		"options": permissionOptions(),
	})
	if err != nil {
		if errors.Is(err, errClientMethod) {
			// Editor does not implement the method — fail-open like a headless agent
			// default, so a partial client is not bricked.
			return mow.PreToolDecision{}, nil
		}
		if ctx.Err() != nil {
			return mow.PreToolDecision{}, ctx.Err()
		}
		return mow.PreToolDecision{Deny: true, Message: "permission request failed: " + err.Error()}, nil
	}

	decision := parsePermissionOutcome(raw)
	switch decision {
	case optAllowAlways:
		a.mu.Lock()
		if a.alwaysAllow == nil {
			a.alwaysAllow = map[string]bool{}
		}
		a.alwaysAllow[name] = true
		a.mu.Unlock()
		return mow.PreToolDecision{}, nil
	case optAllowOnce, "allow":
		return mow.PreToolDecision{}, nil
	case optRejectAlways:
		a.mu.Lock()
		if a.alwaysReject == nil {
			a.alwaysReject = map[string]bool{}
		}
		a.alwaysReject[name] = true
		a.mu.Unlock()
		return mow.PreToolDecision{Deny: true, Message: "denied by user"}, nil
	default:
		return mow.PreToolDecision{Deny: true, Message: "denied by user"}, nil
	}
}

func parsePermissionOutcome(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return optRejectOnce
	}
	var p struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
		OptionID string `json:"optionId"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return optRejectOnce
	}
	if strings.EqualFold(p.Outcome.Outcome, "cancelled") {
		return optRejectOnce
	}
	id := strings.TrimSpace(p.Outcome.OptionID)
	if id == "" {
		id = strings.TrimSpace(p.OptionID)
	}
	switch strings.ToLower(id) {
	case optAllowOnce, "allow", "allow-once":
		return optAllowOnce
	case optAllowAlways, "allow-always", "always":
		return optAllowAlways
	case "reject-always":
		return optRejectAlways
	default:
		return optRejectOnce
	}
}
