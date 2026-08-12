package acp

import (
	"encoding/json"
	"strings"
)

// Permission modes for agent→client session/request_permission calls.
const (
	PermissionReject = "reject"
	PermissionAllow  = "allow"
)

func normalizePermissionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case PermissionAllow, "auto", "grant":
		return PermissionAllow
	default:
		return PermissionReject
	}
}

// effectivePermissionMode returns reject unless the spec or legacy --force opts in.
func effectivePermissionMode(spec AgentSpec) string {
	if m := strings.TrimSpace(spec.PermissionMode); m != "" {
		return normalizePermissionMode(m)
	}
	for _, a := range spec.Command {
		if a == "--force" {
			return PermissionAllow
		}
	}
	return PermissionReject
}

func (c *Client) handleAgentRequest(req request) {
	if c == nil {
		return
	}
	method := strings.TrimSpace(req.Method)
	resp := response{JSONRPC: "2.0", ID: req.ID}

	switch {
	case method == "session/request_permission" || method == "session/requestPermission":
		resp.Result = c.permissionResult(req.Params)
	case strings.HasPrefix(method, "fs/"):
		// Capability not advertised; answer so the peer does not hang.
		resp.Error = &rpcError{
			Code:    errMethod,
			Message: "mow acp client: filesystem access not available (configure peer tools in-process)",
		}
	case strings.HasPrefix(method, "cursor/"):
		resp.Error = &rpcError{
			Code:    errMethod,
			Message: "mow acp client: cursor extension " + method + " not supported (non-interactive delegate)",
		}
	default:
		resp.Error = &rpcError{
			Code:    errMethod,
			Message: "mow acp client: method not supported: " + method,
		}
	}
	// write is encMu-guarded and only touches peer stdin; safe from readLoop.
	_ = c.write(resp)
}

// permissionResult builds an ACP RequestPermissionResponse.
// Spec (RequestPermissionOutcome): cancelled has only {outcome:"cancelled"};
// selected requires {outcome:"selected", optionId:<id from request options>}.
func (c *Client) permissionResult(params json.RawMessage) map[string]any {
	mode := PermissionReject
	if c != nil {
		mode = normalizePermissionMode(c.PermissionMode)
	}
	if mode == PermissionAllow {
		if id := pickAllowOptionID(params); id != "" {
			return map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": id,
				},
			}
		}
		// No parseable options — cancelled still stops the tool without a
		// fabricated optionId (which agents often reject as unknown).
	}
	return map[string]any{
		"outcome": map[string]any{
			"outcome": "cancelled",
		},
	}
}

// pickAllowOptionID chooses an allow_* option from the permission request.
// Prefers allow_once, then allow_always, then any option whose kind/id contains "allow".
func pickAllowOptionID(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Options) == 0 {
		return ""
	}
	var once, always, anyAllow string
	for _, o := range p.Options {
		id := strings.TrimSpace(o.OptionID)
		if id == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(o.Kind))
		switch kind {
		case "allow_once", "allow-once":
			if once == "" {
				once = id
			}
		case "allow_always", "allow-always":
			if always == "" {
				always = id
			}
		default:
			low := strings.ToLower(id)
			if anyAllow == "" && (strings.Contains(kind, "allow") || strings.Contains(low, "allow")) {
				anyAllow = id
			}
		}
	}
	if once != "" {
		return once
	}
	if always != "" {
		return always
	}
	return anyAllow
}

// parseIncomingLine classifies one JSON-RPC line from the peer stdout stream.
func parseIncomingLine(line string) (kind string, req request, resp response, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", req, resp, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
		return "skip", req, resp, true
	}
	if _, hasMethod := probe["method"]; hasMethod {
		if _, hasID := probe["id"]; !hasID {
			return "notification", req, resp, true
		}
		if json.Unmarshal([]byte(line), &req) == nil {
			return "request", req, resp, true
		}
		return "skip", req, resp, true
	}
	if json.Unmarshal([]byte(line), &resp) == nil {
		return "response", req, resp, true
	}
	return "skip", req, resp, true
}
