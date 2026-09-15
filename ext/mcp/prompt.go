package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow/ext"
)

func serverPromptSource(name string) string { return "mcp-prompt:" + name }

// Engine construction snapshots these hooks. Replacing registry entries does
// not rewrite the guidance already captured by another open Engine.
func registerServerPrompt(name, prompt string) {
	source := serverPromptSource(name)
	ext.ClearHookSource(source)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	ext.RegisterUserPromptSource(source, func(ctx context.Context, _ ext.UserPromptEvent) (ext.UserPromptDecision, error) {
		if !ext.IsExtensionActive("mcp:"+name, ext.TurnFromContext(ctx)) {
			return ext.UserPromptDecision{}, nil
		}
		return ext.UserPromptDecision{SystemAppend: fmt.Sprintf("MCP server %s guidance:\n%s", name, prompt)}, nil
	})
}

func clearRegisteredPrompts() {
	registryMu.Lock()
	names := make([]string, 0, len(transports))
	for name := range transports {
		names = append(names, name)
	}
	registryMu.Unlock()
	for _, name := range names {
		ext.ClearHookSource(serverPromptSource(name))
	}
}
