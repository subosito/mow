package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

// diagTimeout bounds one diagnostics pull (including the pack's single
// reconnect attempt). A language server that hangs — indexing a large repo,
// wedged, mid-restart — must never hold up a write that already succeeded.
// Var, not const, so tests can shorten it; never reassigned in production.
var diagTimeout = 10 * time.Second

// maxDiagMessage bounds one diagnostic message so a server that reports a wall
// of text cannot flood the tool result or the event payload.
const maxDiagMessage = 200

// editTools are the tools whose success means a file changed on disk. Read-only
// tools never trigger a diagnostics pull.
var editTools = map[string]bool{"write": true, "edit": true}

// diagFunc pulls diagnostics for one path (injected so tests need no server).
type diagFunc func(ctx context.Context, path string) ([]mow.Diagnostic, error)

// postEditDiagnostics returns a PostTool hook that appends language-server
// findings to a successful write/edit result and emits harness.lsp.diagnostics.
//
// This is the cheapest real feedback loop in the harness: an edit's problems
// come back with the edit, before the model spends a turn running tests. It is
// wired only when the lsp pack is configured — no config, no process, no hook,
// and the tool result is byte-for-byte unchanged.
func postEditDiagnostics(pull diagFunc) ext.PostToolFunc {
	return func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
		if pull == nil || !editTools[e.Name] || e.Denied || e.ExecErr != nil {
			return ext.PostToolDecision{}, nil
		}
		path := argPath(e.Args)
		if path == "" || !supportedFile(path) {
			return ext.PostToolDecision{}, nil
		}
		if eng := mow.EngineFromContext(ctx); eng != nil {
			resolved, err := eng.ResolvePath(path)
			if err != nil {
				slog.Debug("lsp: post-edit diagnostics skipped", "tool", e.Name, "path", path, "err", err)
				return ext.PostToolDecision{}, nil
			}
			path = resolved
		}
		// Own deadline, not the run's: the run context may have hours left.
		pullCtx, cancel := context.WithTimeout(ctx, diagTimeout)
		defer cancel()
		diags, err := pull(pullCtx, path)
		if err != nil {
			// A server that is down, slow, or wedged must never fail an edit
			// that already succeeded: the write happened, diagnostics are a
			// bonus. Return the original result untouched.
			slog.Debug("lsp: post-edit diagnostics skipped",
				"tool", e.Name, "path", path, "err", err,
				"timeout", pullCtx.Err() != nil)
			return ext.PostToolDecision{}, nil
		}
		if len(diags) == 0 {
			return ext.PostToolDecision{}, nil
		}
		count := len(diags)
		// Sort before truncating so the cap can never hide an error behind ten
		// hints. Stable: within a severity the server's order (and so file
		// order) is preserved. Sorting here means every host inherits
		// error-first truncation, not just the ones that re-sort.
		shown := append([]mow.Diagnostic(nil), diags...)
		sort.SliceStable(shown, func(i, j int) bool {
			return mow.SeverityRank(shown[i].Severity) < mow.SeverityRank(shown[j].Severity)
		})
		if len(shown) > mow.MaxLSPDiagnostics {
			shown = shown[:mow.MaxLSPDiagnostics]
		}
		if eng := mow.EngineFromContext(ctx); eng != nil {
			eng.Emit(mow.Event{
				Type:        mow.EventLSPDiagnostics,
				Tool:        e.Name,
				ToolCallID:  e.ToolCallID,
				Path:        path,
				Count:       count,
				Diagnostics: shown,
			})
		}
		return ext.PostToolDecision{Result: e.Result + renderDiagnostics(path, count, shown), Rewrite: true}, nil
	}
}

// renderDiagnostics is the text the model sees appended to its edit result.
func renderDiagnostics(path string, count int, diags []mow.Diagnostic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nlsp diagnostics (%s): %d\n", path, count)
	for _, d := range diags {
		pos := fmt.Sprintf("%s:%d", path, d.Line)
		if d.Column > 0 {
			pos += fmt.Sprintf(":%d", d.Column)
		}
		src := ""
		if d.Source != "" {
			src = " (" + d.Source + ")"
		}
		fmt.Fprintf(&b, "  %s: %s: %s%s\n", pos, d.Severity, d.Message, src)
	}
	if count > len(diags) {
		fmt.Fprintf(&b, "  … %d more\n", count-len(diags))
	}
	return b.String()
}

// argPath reads the "path" argument shared by the write and edit tools.
func argPath(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return ""
	}
	return strings.TrimSpace(a.Path)
}

// supportedFile keeps the pull to languages an LSP server is plausibly serving,
// so a markdown or JSON write does not pay for a round trip.
func supportedFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".rs", ".ts", ".tsx", ".js", ".jsx", ".py", ".c", ".h", ".cc", ".cpp", ".java", ".rb", ".zig":
		return true
	}
	return false
}
