package llm

import (
	"path"
	"strings"
)

// effectiveSystemPrefix returns prefix entries that apply to model.
// Empty patterns → always apply when prefix is non-empty.
// Non-empty patterns → case-insensitive path.Match globs (*, ?).
func effectiveSystemPrefix(prefix, patterns []string, model string) []string {
	if len(prefix) == 0 {
		return nil
	}
	if !modelMatchesAny(model, patterns) {
		return nil
	}
	return prefix
}

// modelMatchesAny reports whether model matches any pattern.
// Empty / all-blank patterns → true (apply for all models).
func modelMatchesAny(model string, patterns []string) bool {
	var nonEmpty int
	m := strings.ToLower(strings.TrimSpace(model))
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		nonEmpty++
		ok, err := path.Match(p, m)
		if err == nil && ok {
			return true
		}
	}
	return nonEmpty == 0
}

// activeSystemPrefix is SystemPrefix after model-glob filtering.
func (c *Client) activeSystemPrefix() []string {
	if c == nil {
		return nil
	}
	return effectiveSystemPrefix(c.SystemPrefix, c.SystemPrefixModels, c.Model)
}

// messagesWithSystemPrefix prepends active prefix entries as role=system
// messages. Anthropic-messages is skipped: that wire maps prefix to dedicated
// top-level system text blocks (see anthropicSystemField) so segments stay
// separate instead of being joined into one system string.
func (c *Client) messagesWithSystemPrefix(messages []Message) []Message {
	if c == nil || NormalizeWire(c.Wire) == WireAnthropicMsg {
		return messages
	}
	prefix := c.activeSystemPrefix()
	if len(prefix) == 0 {
		return messages
	}
	out := make([]Message, 0, len(prefix)+len(messages))
	for _, p := range prefix {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, Message{Role: "system", Content: s})
		}
	}
	if len(out) == 0 {
		return messages
	}
	return append(out, messages...)
}
