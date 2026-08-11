// Package llm talks to OpenAI-compatible (and optionally Anthropic) chat APIs.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Message is a chat message in OpenAI-ish shape.
//
// Content uses omitempty for session/history JSON. OpenAI chat/completions
// requests go through toOpenAIMessages, which always emits content as a string
// (including "") so gateways with a strict MessageContent enum accept
// tool-call turns and empty tool results.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Synthetic marks host-injected user messages (thrash/explore warnings,
	// mid-turn steer) that never came from the user's prompt. They are real
	// history for the model, but Engine.Rewind must skip them so edit/retry
	// lands on the user's own prompt. Never sent on the wire.
	Synthetic bool `json:"-"`
	// StopReason is why the provider stopped generating (OpenAI finish_reason /
	// Anthropic stop_reason). "max_tokens" or "length" means the reply was
	// truncated at the token limit. Response-only; never sent on the wire.
	StopReason string `json:"-"`
	// Usage is provider-reported token counts for the call that produced this
	// message (zero when the provider sent none). Response-only.
	Usage Usage `json:"-"`
	// ProviderCalls records provider-executed tool calls (web_search_call,
	// code_interpreter_call, …) observed in this response. The provider ran
	// them server-side; they must never appear in ToolCalls, or the agent
	// loop would try to execute tools it does not have. Reporting only —
	// response-only, never sent on the wire, never replayed.
	ProviderCalls []ProviderCall `json:"-"`
}

// openAIMessage is the chat/completions wire shape. Content is always a JSON
// string (never omitted, never null). Many OpenAI-compatible gateways type
// content as an untagged enum Text(String)|Parts([...]) with no null variant;
// assistant tool-call turns and empty tool results then 400 with
// "data did not match any variant of untagged enum MessageContent" if content
// is missing or null. Emitting "" is accepted by OpenAI and those gateways.
type openAIMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// toOpenAIMessages maps internal history to the OpenAI wire shape.
func toOpenAIMessages(in []Message) []openAIMessage {
	out := make([]openAIMessage, len(in))
	for i, m := range in {
		out[i] = openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if len(m.ToolCalls) == 0 {
			continue
		}
		tcs := make([]ToolCall, len(m.ToolCalls))
		for j, tc := range m.ToolCalls {
			tcs[j] = tc
			if tcs[j].Type == "" {
				tcs[j].Type = "function"
			}
			// Empty arguments are invalid JSON for most tool schemas; models
			// sometimes stream a name with no arg chunks.
			if strings.TrimSpace(tcs[j].Function.Arguments) == "" {
				tcs[j].Function.Arguments = "{}"
			}
		}
		out[i].ToolCalls = tcs
	}
	return out
}

// Usage counts provider-reported tokens for one chat call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// CachedInputTokens is the portion of InputTokens the provider served from
	// its prompt cache. It is a SUBSET of InputTokens, not an addition —
	// every wire mow speaks reports the total first and the cached share
	// separately (OpenAI prompt_tokens_details.cached_tokens, Anthropic
	// cache_read_input_tokens).
	//
	// Cached input is billed at a large discount (commonly ~10% of the input
	// rate), so cost computed from InputTokens alone materially overstates
	// spend on a stable prefix — which is exactly the shape of an agent loop.
	// Zero when the provider reports none.
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	// CacheWriteInputTokens is the portion of InputTokens the provider wrote
	// into its prompt cache this call. Also a SUBSET of InputTokens, and
	// disjoint from CachedInputTokens: a token is either served from cache
	// (read) or newly inserted into it (write), never both.
	//
	// Writes are billed ABOVE plain input (Anthropic ~1.25x), so they are the
	// opposite of a discount. They matter far more than the rate suggests
	// because of write amplification: whenever the cached prefix is
	// invalidated — a model switch, a compaction that rewrites history, a
	// change to the tool set — the next call re-inserts the entire
	// conversation as a write rather than appending a small delta. On a long
	// session that turns into the single largest line item, which is exactly
	// why it must be visible rather than folded into the input total.
	CacheWriteInputTokens int `json:"cache_write_input_tokens,omitempty"`
	// SourcesUsed counts provider-side sources cited by server-executed tools
	// (e.g. Responses web_search num_sources_used). Zero when the provider
	// sent none.
	SourcesUsed int `json:"sources_used,omitempty"`
	// ServerSideToolCalls counts calls the provider executed server-side
	// (Responses num_server_side_tool_calls). Zero when the provider sent
	// none.
	ServerSideToolCalls int `json:"server_side_tool_calls,omitempty"`
}

// Zero reports whether no counts were recorded (provider sent no usage).
func (u Usage) Zero() bool { return u.InputTokens == 0 && u.OutputTokens == 0 }

// Add returns the element-wise sum (accumulating usage across loop turns).
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:           u.InputTokens + o.InputTokens,
		OutputTokens:          u.OutputTokens + o.OutputTokens,
		CachedInputTokens:     u.CachedInputTokens + o.CachedInputTokens,
		CacheWriteInputTokens: u.CacheWriteInputTokens + o.CacheWriteInputTokens,
		SourcesUsed:           u.SourcesUsed + o.SourcesUsed,
		ServerSideToolCalls:   u.ServerSideToolCalls + o.ServerSideToolCalls,
	}
}

// FreshInputTokens is InputTokens minus the cache read and write shares: the
// part billed at the plain input rate. Clamped at zero so a provider reporting
// shares larger than the total (seen on some gateways) cannot produce a
// negative.
func (u Usage) FreshInputTokens() int {
	used := u.CachedInputTokens + u.CacheWriteInputTokens
	if used >= u.InputTokens {
		return 0
	}
	return u.InputTokens - used
}

// Truncated reports whether the provider cut the reply at its token limit.
func (m Message) Truncated() bool {
	return m.StopReason == "max_tokens" || m.StopReason == "length"
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
	// ThoughtSignature is optional provider metadata on a tool_call that must
	// be echoed when replaying that call in later turns. Empty on most
	// providers; omitempty keeps ordinary payloads clean.
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// FunctionCall holds name + JSON arguments string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ProviderCall is one provider-executed tool call observed in a response
// (web_search_call, code_interpreter_call, …). The provider ran it
// server-side; this is observability metadata, not something mow executes.
type ProviderCall struct {
	// Type is the output item type as sent by the provider ("web_search_call").
	Type string `json:"type"`
	// ID is the provider item id when present (for log correlation).
	ID string `json:"id,omitempty"`
	// Status is the provider-reported call status when present
	// ("completed", "in_progress", "failed", …).
	Status string `json:"status,omitempty"`
}

// ToolSpec is exposed to the model as a function tool.
type ToolSpec struct {
	Type     string           `json:"type"`
	Function ToolSpecFunction `json:"function"`
}

// ToolSpecFunction is the OpenAI tools[].function object.
type ToolSpecFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Client is a multi-wire chat client (chat-completions, responses, or anthropic-messages).
type Client struct {
	// Wire is the client protocol id (see WireOpenAIChat, WireOpenAIResponses, WireAnthropicMsg).
	Wire         string
	BaseURL      string
	APIKey       string
	Model        string
	HTTP         *http.Client
	ExtraHeaders map[string]string
	// Stream enables SSE token deltas when supported by the wire.
	Stream bool
	// FirstByteTimeout bounds how long a streaming call waits for the first
	// response byte/headers (the ResponseHeaderTimeout of the stream HTTP
	// client). Zero = defaultFirstByteTimeout. Long-reasoning models can
	// spend minutes thinking before the first SSE chunk; the default matches
	// streamIdleTimeout so "no bytes for X" means the same X before and after
	// the first byte. A full first-byte timeout is a hard, non-retried
	// failure. Only consulted when c.HTTP is nil (mow's own stream client);
	// a caller-supplied HTTP client keeps its own semantics.
	FirstByteTimeout time.Duration `json:"-"`
	// CallTimeout bounds a single non-streaming call (one attempt). Zero =
	// defaultCallTimeout (120s). Only consulted when c.HTTP is nil.
	CallTimeout time.Duration `json:"-"`
	// MaxTokens caps the response length on wires that require it
	// (anthropic-messages, openai-responses max_output_tokens). Zero means
	// provider default (8192 for Anthropic; omit for Responses).
	MaxTokens int
	// PromptCache enables provider prompt caching where the wire supports an
	// explicit breakpoint (anthropic-messages: cache_control on system, tools,
	// and the last message). OpenAI caches automatically and ignores this.
	PromptCache bool

	// CacheTTL selects the ephemeral cache lifetime for anthropic-messages.
	// Empty means the provider default (5 minutes); "1h" buys the longer
	// window, which pays off in interactive sessions whose think-time gaps
	// routinely exceed 5 minutes — every lapsed window re-charges the whole
	// prefix as fresh input.
	CacheTTL string
	// SystemPrefix is optional text segments prepended before the compiled
	// system prompt (harness + AGENTS.md / skills). Each entry is a separate
	// segment. Typical use: product identity / provider preambles. May override
	// self-name/tone; harness operating rules still apply. On anthropic-messages
	// they become separate system text blocks; on other wires they are leading
	// role=system messages. Configure via llm.system_prefix — mow does not
	// hardcode vendor text.
	SystemPrefix []string
	// SystemPrefixModels are case-insensitive globs against Client.Model.
	// Empty = always apply SystemPrefix when set. Non-empty = apply only on match.
	SystemPrefixModels []string
	// Effort is the canonical reasoning intensity (none|low|medium|high, or
	// catalog-advertised values). Empty = provider/gateway default.
	// Applied via body fields; gateways may map effort to upstream model tiers.
	Effort string
	// EffortPinned marks Effort as an explicit operator choice (config
	// llm.effort or SetEffort) rather than a catalog default. A model switch
	// keeps a pinned effort when the new model allows it, but otherwise adopts
	// the new model's default_effort.
	EffortPinned bool
	// CatalogModels is the lean model catalog from ListModels (id → efforts metadata).
	// When the active model has Efforts set, SetEffort / ResolveEffort use that list
	// instead of the static none|low|medium|high set.
	CatalogModels map[string]ModelInfo
	// NativeTools are provider-executed tool declarations merged into the
	// request "tools" array (e.g. {"type":"web_search"}). The provider runs
	// these itself: mow never opens a socket for them, they are not policy
	// jailed, and they do not appear as tool.start/tool.end events. Empty
	// (the default) sends nothing.
	//
	// Capability is per model, not per wire — declaring a tool a model cannot
	// execute makes it emit a call nothing answers, which leaks into the reply
	// as stray tokens. Configure only for models known to support it.
	NativeTools []map[string]any
}

// ChatRequest is the outbound chat body (subset).
type ChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []ToolSpec      `json:"tools,omitempty"`
}

// ChatResponse is the inbound chat body (subset).
type ChatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Chat sends messages and optional tools; returns the assistant message.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	return c.ChatWithDelta(ctx, messages, tools, nil)
}

// ChatWithDelta is Chat with optional content delta callback (SSE when stream/onDelta).
func (c *Client) ChatWithDelta(ctx context.Context, messages []Message, tools []ToolSpec, onDelta DeltaFn) (Message, error) {
	return c.ChatWithStream(ctx, messages, tools, StreamHooks{OnContent: onDelta})
}

// ChatWithStream is Chat with content and reasoning SSE callbacks.
func (c *Client) ChatWithStream(ctx context.Context, messages []Message, tools []ToolSpec, hooks StreamHooks) (Message, error) {
	stream := c.Stream || hooks.OnContent != nil || hooks.OnReasoning != nil
	// Leading system segments for non-Anthropic wires (Anthropic uses system blocks).
	messages = c.messagesWithSystemPrefix(messages)
	switch NormalizeWire(c.Wire) {
	case WireAnthropicMsg:
		if stream {
			return c.chatAnthropicStream(ctx, messages, tools, hooks)
		}
		return c.chatAnthropic(ctx, messages, tools)
	case WireOpenAIResponses:
		if stream {
			return c.chatOpenAIResponsesStream(ctx, messages, tools, hooks)
		}
		return c.chatOpenAIResponses(ctx, messages, tools)
	default: // WireOpenAIChat
		if stream {
			return c.ChatStreamHooks(ctx, messages, tools, hooks)
		}
		return c.chatOpenAI(ctx, messages, tools)
	}
}

func (c *Client) chatOpenAI(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("llm: api key required")
	}
	if c.Model == "" {
		return Message{}, fmt.Errorf("llm: model required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	url := base + "/chat/completions"
	if strings.HasSuffix(base, "/chat/completions") {
		url = base
	}

	body := ChatRequest{Model: c.requestModel(), Messages: toOpenAIMessages(messages), Tools: tools}
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
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	status, respBody, err := c.doJSON(req)
	if err != nil {
		return Message{}, err
	}
	var parsed ChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Message{}, fmt.Errorf("llm: decode: %w (status %d body %s)", err, status, truncate(string(respBody), 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Message{}, fmt.Errorf("llm: %s", parsed.Error.Message)
	}
	if status < 200 || status >= 300 {
		return Message{}, fmt.Errorf("llm: HTTP %d: %s", status, truncate(string(respBody), 300))
	}
	if len(parsed.Choices) == 0 {
		return Message{}, fmt.Errorf("llm: empty choices")
	}
	msg := parsed.Choices[0].Message
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	msg.StopReason = parsed.Choices[0].FinishReason
	msg.Usage = Usage{
		InputTokens:       parsed.Usage.PromptTokens,
		OutputTokens:      parsed.Usage.CompletionTokens,
		CachedInputTokens: cachedFromDetails(parsed.Usage.PromptTokensDetails),
	}
	return msg, nil
}

// truncate clamps s to n bytes, cutting on a rune boundary.
//
// Callers pass untrusted external bytes (HTTP error bodies from a gateway or
// provider), so a naive s[:n] can land mid-rune and emit invalid UTF-8 into an
// error string that then flows to logs, sessions, and the model.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// OneShot returns a copy of c with prompt caching off, for a call whose prefix
// will never be sent again: a compaction summary, a single review pass, a
// delegated sub-task.
//
// Such a call writes a cache entry nothing will read, and providers bill a
// cache write above plain input (Anthropic ~1.25x). Marking breakpoints on a
// prefix with no second call is therefore a pure surcharge — small per call,
// but these fire on a schedule for the life of a session.
func (c *Client) OneShot() *Client {
	if c == nil {
		return nil
	}
	cp := *c
	cp.PromptCache = false
	cp.CacheTTL = ""
	return &cp
}

// cachedFromDetails reads the cached-token share from an OpenAI-shaped
// prompt_tokens_details block. Nil-safe: most gateways omit it entirely.
func cachedFromDetails(d *struct {
	CachedTokens int `json:"cached_tokens"`
}) int {
	if d == nil {
		return 0
	}
	return d.CachedTokens
}
