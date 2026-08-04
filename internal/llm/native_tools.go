package llm

import (
	"log/slog"
	"strings"
	"sync"
)

// activeNativeTools returns the provider-executed tool declarations to merge
// into the request.
//
// Local config wins when set, so an operator can disable a catalog tool or add
// one the gateway does not publish. Otherwise the catalog decides: capability
// is a property of the model, and the gateway already knows it, so clients do
// not each have to repeat the same list — nor can they claim a tool the model
// cannot run.
func (c *Client) activeNativeTools() []map[string]any {
	if c == nil {
		return nil
	}
	if len(c.NativeTools) > 0 {
		return c.NativeTools
	}
	if len(c.CatalogModels) == 0 {
		return nil
	}
	id := strings.TrimSpace(StripEffortTiers(c.Model))
	if info, ok := c.CatalogModels[strings.ToLower(id)]; ok && len(info.NativeTools) > 0 {
		return info.NativeTools
	}
	if info, ok := c.CatalogModels[id]; ok && len(info.NativeTools) > 0 {
		return info.NativeTools
	}
	return nil
}

// mergeNativeTools appends provider-executed tool declarations to whatever the
// wire already built, skipping ones already present by "type". Local function
// tools and native tools share one array on every wire, so this must add to
// the existing value rather than replace it — overwriting would silently drop
// the agent's own tools and strand the loop.
func mergeNativeTools(existing any, native []map[string]any) []any {
	var out []any
	seen := map[string]bool{}
	if cur, ok := existing.([]any); ok {
		out = append(out, cur...)
		for _, it := range cur {
			if mm, ok := it.(map[string]any); ok {
				if t, _ := mm["type"].(string); t != "" {
					seen[t] = true
				}
			}
		}
	}
	for _, nt := range native {
		if len(nt) == 0 {
			continue
		}
		if t, _ := nt["type"].(string); t != "" {
			if seen[t] {
				continue
			}
			seen[t] = true
		}
		out = append(out, nt)
	}
	return out
}

// nativeToolWarned dedupes the unsupported-wire warning by model. The guard
// lives here rather than on Client because Client is copied by value (the
// engine clones it per model switch), which rules out any embedded sync type.
var nativeToolWarned sync.Map // model -> struct{}

// warnNativeToolsUnsupported logs once per model that native tools were
// configured on a wire that cannot carry them, so a long agent loop does not
// repeat it on every call.
func (c *Client) warnNativeToolsUnsupported() {
	if _, dup := nativeToolWarned.LoadOrStore(c.Model, struct{}{}); dup {
		return
	}
	types := make([]string, 0, len(c.activeNativeTools()))
	for _, t := range c.activeNativeTools() {
		if s, _ := t["type"].(string); s != "" {
			types = append(types, s)
		}
	}
	slog.Warn("llm: native_tools ignored on this wire",
		"wire", WireOpenAIChat,
		"model", c.Model,
		"tools", strings.Join(types, ","),
		"detail", "chat-completions has no server-tool channel; the provider drops the declaration and the model answers from training data. Use openai-responses or anthropic-messages.")
}
