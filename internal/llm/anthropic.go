package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropicMaxTokens resolves the reply cap the anthropic-messages wire
// requires in every request.
//
// Priority: explicit llm.max_tokens → the model's published generation cap →
// a conservative floor. The catalog step matters: the historical hard-coded
// 8192 is a 2023-era number, and a current model advertising 128000 was being
// cut at 6% of its capacity. A truncated reply is not just a short answer — if
// it cuts a tool call the loop refuses the batch and, after a couple of
// retries, fails the run.
func (c *Client) anthropicMaxTokens() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	if info, ok := c.CatalogEntry(c.Model); ok && info.MaxOutputTokens > 0 {
		return info.MaxOutputTokens
	}
	return defaultAnthropicMaxTokens
}

// defaultAnthropicMaxTokens is the last-resort reply cap when neither config
// nor the catalog says anything. The wire requires the field, so there is no
// "omit and let the provider decide" option here. Kept at the historical value
// because raising it blind risks a 400 from a model that cannot honour it —
// gateways that publish max_output_tokens get their real cap above.
const defaultAnthropicMaxTokens = 8192

// chatAnthropic maps OpenAI-shaped messages/tools to Anthropic Messages API.
func (c *Client) chatAnthropic(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	url := anthropicMessagesURL(c.BaseURL)

	system, anthMsgs := toAnthropicMessages(messages)
	if c.PromptCache {
		cacheLastMessage(anthMsgs, c.CacheTTL)
	}
	body := map[string]any{
		"model":      c.requestModel(),
		"max_tokens": c.anthropicMaxTokens(),
		"messages":   anthMsgs,
	}
	if sys := anthropicSystemField(c.activeSystemPrefix(), system, c.PromptCache, c.CacheTTL); sys != nil {
		body["system"] = sys
	}
	if atools := toAnthropicTools(tools); len(atools) > 0 {
		if c.PromptCache {
			cacheLastTool(atools, c.CacheTTL)
		}
		body["tools"] = atools
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	raw, err = c.finalizeChatBody(raw)
	if err != nil {
		return Message{}, err
	}
	req, err := newJSONRequest(ctx, http.MethodPost, url, raw)
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}
	status, respBody, err := c.doJSON(req)
	if err != nil {
		return Message{}, err
	}
	if status < 200 || status >= 300 {
		return Message{}, fmt.Errorf("llm: anthropic HTTP %d: %s", status, truncate(string(respBody), 300))
	}
	var parsed struct {
		Content []struct {
			Type  string `json:"type"`
			Text  string `json:"text,omitempty"`
			ID    string `json:"id,omitempty"`
			Name  string `json:"name,omitempty"`
			Input any    `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			// ServerToolUse counts tools Anthropic ran itself (native_tools).
			ServerToolUse *struct {
				WebSearchRequests int `json:"web_search_requests"`
			} `json:"server_tool_use,omitempty"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Message{}, fmt.Errorf("llm: anthropic decode: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Message{}, fmt.Errorf("llm: %s", parsed.Error.Message)
	}
	msg := Message{Role: "assistant", StopReason: parsed.StopReason}
	// When caching is active Anthropic reports non-cached input in input_tokens
	// and the cached prefix separately; sum them so the total input is honest.
	msg.Usage = Usage{
		InputTokens:  parsed.Usage.InputTokens + parsed.Usage.CacheCreationInputTokens + parsed.Usage.CacheReadInputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		// Reads only: a cache WRITE is billed above plain input, so counting
		// it as cached would understate cost.
		CachedInputTokens: parsed.Usage.CacheReadInputTokens,
	}
	if st := parsed.Usage.ServerToolUse; st != nil {
		// Anthropic reports server-side work as a request count rather than a
		// call list, so this is the only signal that a native tool ran.
		msg.Usage.ServerSideToolCalls = st.WebSearchRequests
	}
	var textParts []string
	for _, b := range parsed.Content {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "tool_use":
			args, _ := json.Marshal(b.Input)
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      b.Name,
					Arguments: string(args),
				},
			})
		case "server_tool_use", "web_search_tool_result":
			// Anthropic ran this itself (native_tools). It must never join
			// ToolCalls: the name is Anthropic's server tool, not one the
			// agent has, so the loop would fail trying to execute it — and
			// replaying a result for it would corrupt the next request.
			// Record for reporting only, like the Responses *_call items.
			msg.ProviderCalls = append(msg.ProviderCalls, ProviderCall{
				Type: b.Type,
				ID:   b.ID,
			})
		}
	}
	msg.Content = strings.Join(textParts, "")
	return msg, nil
}

// Prompt caching (anthropic-messages): cache_control breakpoints tell Anthropic
// to cache the request prefix up to and including the marked block. We mark the
// three stable/large prefixes — system prompt, tool definitions, and the last
// message — so repeated turns (and the many LLM calls inside one agent loop)
// re-read the cached prefix (~90% cheaper input) instead of re-sending it. Max
// four breakpoints are allowed; we use at most three.
func ephemeralCacheControl(ttl string) map[string]any {
	cc := map[string]any{"type": "ephemeral"}
	if ttl != "" {
		cc["ttl"] = ttl
	}
	return cc
}

// anthropicSystemField returns the request "system" value.
//
// prefix entries become separate text blocks before the main system string so
// each llm.system_prefix item stays its own segment (not joined with \n\n).
//
// Shapes:
//   - no prefix, no cache → plain string
//   - no prefix, cache → one text block with cache_control
//   - prefix set → block array
func anthropicSystemField(prefix []string, system string, cache bool, ttl string) any {
	var blocks []map[string]any
	for _, p := range prefix {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": p})
	}
	if s := strings.TrimSpace(system); s != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": s})
	}
	if len(blocks) == 0 {
		return nil
	}
	if len(blocks) == 1 && !cache {
		return blocks[0]["text"]
	}
	if cache {
		// A breakpoint caches everything up to and including the marked block,
		// so only the last system block needs the marker. Marking every block
		// would waste breakpoint slots and — with two or more system_prefix
		// entries — exceed Anthropic's 4-breakpoint limit shared with the tool
		// and last-message markers, failing the request outright.
		blocks[len(blocks)-1]["cache_control"] = ephemeralCacheControl(ttl)
	}
	return blocks
}

// cacheLastTool marks the final tool so the (stable) tool definitions cache
// alongside the system prompt.
func cacheLastTool(tools []map[string]any, ttl string) {
	if len(tools) == 0 {
		return
	}
	tools[len(tools)-1]["cache_control"] = ephemeralCacheControl(ttl)
}

// cacheLastMessage marks the last content block of the last message, so a
// growing multi-turn history caches incrementally (each turn's tail is a cache
// hit on the next call). Promotes a plain string body to a block to carry the
// marker.
func cacheLastMessage(msgs []map[string]any, ttl string) {
	if len(msgs) == 0 {
		return
	}
	last := msgs[len(msgs)-1]
	switch c := last["content"].(type) {
	case string:
		last["content"] = []map[string]any{{
			"type":          "text",
			"text":          c,
			"cache_control": ephemeralCacheControl(ttl),
		}}
	case []map[string]any:
		if len(c) > 0 {
			c[len(c)-1]["cache_control"] = ephemeralCacheControl(ttl)
		}
	}
}

func toAnthropicMessages(messages []Message) (system string, out []map[string]any) {
	for _, m := range messages {
		switch m.Role {
		case "system":
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
		case "user":
			out = append(out, map[string]any{"role": "user", "content": m.Content})
		case "assistant":
			var blocks []map[string]any
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
				if input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": ""})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			// Anthropic wants tool_result on user role
			out = append(out, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     m.Content,
				}},
			})
		}
	}
	return system, out
}

func toAnthropicTools(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var schema any
		if len(t.Function.Parameters) > 0 {
			_ = json.Unmarshal(t.Function.Parameters, &schema)
		}
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":         t.Function.Name,
			"description":  t.Function.Description,
			"input_schema": schema,
		})
	}
	return out
}
