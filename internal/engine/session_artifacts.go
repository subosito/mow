package engine

import "fmt"

// SaveToolResult persists one tool result beside the current session and
// returns its stable ID. It is exposed on Engine so optional extensions can
// store large results without importing internal/session.
func (e *Engine) SaveToolResult(tool, body string) (string, error) {
	if e == nil || e.sess == nil {
		return "", fmt.Errorf("engine: no session")
	}
	return e.sess.SaveToolResult(tool, body)
}

// SessionDir returns the active session's directory, or "" for a sessionless
// engine. Optional extensions use it to locate session-scoped artifacts.
func (e *Engine) SessionDir() string {
	if e == nil || e.sess == nil {
		return ""
	}
	return e.sess.Dir
}

// StoredToolResult reads a stored tool result by ID. Missing or pruned results
// fail explicitly.
func (e *Engine) StoredToolResult(id string) (string, error) {
	if e == nil || e.sess == nil {
		return "", fmt.Errorf("engine: no session")
	}
	return e.sess.GetToolResult(id)
}
