package llm

import (
	"context"
	"net/http"
	"strings"
)

// Client wire ids for llm.wire config.
// OpenAI-compatible gateways often select protocol by path; body "model" selects the model.
const (
	// WireOpenAIChat is POST /v1/chat/completions (default agent path).
	WireOpenAIChat = "openai-chat-completions"
	// WireOpenAIResponses is POST /v1/responses (OpenAI Responses API).
	WireOpenAIResponses = "openai-responses"
	// WireAnthropicMsg is POST /v1/messages.
	WireAnthropicMsg = "anthropic-messages"
)

// NormalizeWire canonicalizes a wire id. Empty → OpenAI chat completions.
// Unknown values are returned trimmed lower-case for a clear config error later.
func NormalizeWire(w string) string {
	w = strings.ToLower(strings.TrimSpace(w))
	if w == "" {
		return WireOpenAIChat
	}
	// Singular alias (common typo / gateway label).
	if w == "openai-response" {
		return WireOpenAIResponses
	}
	return w
}

// IsKnownChatWire reports whether mow can speak this wire for chat/tools.
func IsKnownChatWire(w string) bool {
	switch NormalizeWire(w) {
	case WireOpenAIChat, WireOpenAIResponses, WireAnthropicMsg:
		return true
	default:
		return false
	}
}

// Optional HTTP attribution labels (ignored by plain providers).
// Gateways that care can map these into their own observability slots.
const (
	HeaderActor     = "X-Mow-Actor"
	HeaderSession   = "X-Mow-Session"
	HeaderComponent = "X-Mow-Component"
)

type sessionKey struct{}

// WithSessionID returns ctx carrying the mow session id. Requests made with
// that context send it as X-Mow-Session, so a gateway can keep server-side
// conversation state (e.g. an agent per session) without guessing from the
// transcript.
func WithSessionID(ctx context.Context, sid string) context.Context {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, sid)
}

// SessionIDFrom returns the session id set by WithSessionID, or "".
func SessionIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sid, _ := ctx.Value(sessionKey{}).(string)
	return sid
}

// applyHeaders sets the client's extra headers, then the session header from
// the request context unless the operator configured one explicitly.
func (c *Client) applyHeaders(req *http.Request) {
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}
	if req.Header.Get(HeaderSession) == "" {
		if sid := SessionIDFrom(req.Context()); sid != "" {
			req.Header.Set(HeaderSession, sid)
		}
	}
}
