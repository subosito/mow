package llm

import "strings"

// Client wire ids for llm.wire config.
// OpenAI-compatible gateways often select protocol by path; body "model" selects the model.
const (
	// WireOpenAIChat is POST /v1/chat/completions (default agent path).
	WireOpenAIChat = "openai-chat-completions"
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
	return w
}

// IsKnownChatWire reports whether mow can speak this wire for chat/tools.
func IsKnownChatWire(w string) bool {
	switch NormalizeWire(w) {
	case WireOpenAIChat, WireAnthropicMsg:
		return true
	default:
		return false
	}
}
