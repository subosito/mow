package engine

import (
	"fmt"
	"net/http"

	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/tools"
)

// ProviderConnection is host-only connection configuration for extensions.
// It contains credentials and must never be logged or exposed as a model tool.
type ProviderConnection struct {
	BaseURL    string
	APIKey     string
	Headers    map[string]string
	HTTPClient *http.Client
}

// ToolEnabled reports whether the resolved tools.enable list includes name.
// Extensions can use it for opt-in registration; it does not grant permissions.
func (e *Engine) ToolEnabled(name string) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg != nil && e.cfg.ToolEnabled(name)
}

// ProviderConnection returns a copy of this engine's resolved connection.
// It does not load additional files or initialize a deferred chat client.
func (e *Engine) ProviderConnection() ProviderConnection {
	if e == nil {
		return ProviderConnection{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var c ProviderConnection
	if e.cfg != nil {
		c = ProviderConnection{BaseURL: e.cfg.LLM.BaseURL, APIKey: e.cfg.ResolveAPIKey(), Headers: cloneStringMap(e.cfg.LLM.Headers), HTTPClient: e.opt.HTTPClient}
	}
	if e.client != nil {
		c = ProviderConnection{BaseURL: e.client.BaseURL, APIKey: e.client.APIKey, Headers: cloneStringMap(e.client.ExtraHeaders), HTTPClient: e.client.HTTP}
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	if e.sid != "" {
		c.Headers[llm.HeaderSession] = e.sid
	}
	return c
}

// ReadFile reads through this engine's filesystem jail and configured read cap.
func (e *Engine) ReadFile(path string) (string, []byte, error) {
	if e == nil || e.pol == nil {
		return "", nil, fmt.Errorf("mow: engine has no filesystem policy")
	}
	return tools.ReadFileJailed(e.pol, path)
}

// WriteFile writes through this engine's actual policy, including read-only
// roots. Unlike workspace-only helpers it also enforces the write capability.
func (e *Engine) WriteFile(path string, data []byte) (string, error) {
	if e == nil || e.pol == nil {
		return "", fmt.Errorf("mow: engine has no filesystem policy")
	}
	if !e.pol.AllowWrite {
		return "", fmt.Errorf("mow: write capability required")
	}
	return tools.WriteFileJailed(e.pol, path, data, 0o644)
}
