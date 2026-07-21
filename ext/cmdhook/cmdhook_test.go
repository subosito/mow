package cmdhook

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeHooksJSON(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hooks", "hooks.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, c Config) *bridge {
	t.Helper()
	b, err := load(c)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("bridge not configured")
	}
	return b
}

func TestPreToolUseDecisions(t *testing.T) {
	root := t.TempDir()
	// deny.sh emits a Claude permissionDecision; ctx.sh emits additionalContext;
	// blocker.sh uses the exit-2 contract.
	script := func(name, body string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	deny := script("deny.sh", `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"not on my watch"}}'`)
	ctxs := script("ctx.sh", `echo '{"hookSpecificOutput":{"additionalContext":"use the sandbox"}}'`)
	block2 := script("block2.sh", `echo "stderr says no" >&2; exit 2`)

	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[
		{"matcher":"Bash","hooks":[{"type":"command","command":"`+deny+`"}]},
		{"matcher":"Read","hooks":[{"type":"command","command":"`+ctxs+`"}]},
		{"matcher":"Grep","hooks":[{"type":"command","command":"`+block2+`"}]}
	]}}`)
	b := mustLoad(t, Config{Root: root})

	// Bash matcher → deny decision.
	out := b.run(context.Background(), "PreToolUse", "Bash",
		map[string]any{"hook_event_name": "PreToolUse", "cwd": root})
	if !out.blocked || !strings.Contains(out.reason, "not on my watch") {
		t.Fatalf("deny: %+v", out)
	}
	// Read matcher → context only.
	out = b.run(context.Background(), "PreToolUse", "Read",
		map[string]any{"hook_event_name": "PreToolUse", "cwd": root})
	if out.blocked || len(out.contexts) != 1 || out.contexts[0] != "use the sandbox" {
		t.Fatalf("context: %+v", out)
	}
	// Grep matcher → exit-2 block with stderr reason.
	out = b.run(context.Background(), "PreToolUse", "Grep",
		map[string]any{"hook_event_name": "PreToolUse", "cwd": root})
	if !out.blocked || !strings.Contains(out.reason, "stderr says no") {
		t.Fatalf("exit2: %+v", out)
	}
	// Unmatched tool → nothing fires.
	out = b.run(context.Background(), "PreToolUse", "Write",
		map[string]any{"hook_event_name": "PreToolUse", "cwd": root})
	if out.blocked || len(out.contexts) != 0 {
		t.Fatalf("unmatched: %+v", out)
	}
}

func TestPayloadReachesStdin(t *testing.T) {
	root := t.TempDir()
	captured := filepath.Join(root, "captured.json")
	cap := filepath.Join(root, "cap.sh")
	if err := os.WriteFile(cap, []byte("#!/bin/sh\ncat > "+captured+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[
		{"matcher":"","hooks":[{"type":"command","command":"`+cap+`"}]}
	]}}`)
	b := mustLoad(t, Config{Root: root})

	args := json.RawMessage(`{"command":"ls -la"}`)
	b.run(context.Background(), "PreToolUse", "Bash", map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      args,
		"cwd":             root,
		"session_id":      "sess-1",
	})
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Event     string          `json:"hook_event_name"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		CWD       string          `json:"cwd"`
		SessionID string          `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("stdin not JSON: %v (%s)", err, raw)
	}
	if got.Event != "PreToolUse" || got.ToolName != "Bash" ||
		!strings.Contains(string(got.ToolInput), "ls -la") ||
		got.CWD != root || got.SessionID != "sess-1" {
		t.Fatalf("payload=%s", raw)
	}
}

func TestClaudeToolNameMapping(t *testing.T) {
	for in, want := range map[string]string{
		"read": "Read", "bash": "Bash", "grep": "Grep", "write": "Write",
		"edit": "Edit", "glob": "Glob",
		"mcp_srv_lookup": "mcp__srv_lookup",
		"acp_delegate":   "acp_delegate",
	} {
		if got := claudeToolName(in); got != want {
			t.Errorf("claudeToolName(%q)=%q want %q", in, got, want)
		}
	}
}

// TestContextModeRealHook drives the actual context-mode plugin hook through
// the bridge — the compatibility check this pack exists for. Requires node
// and a cloned context-mode repo (CMDHOOK_CONTEXT_MODE env); skipped otherwise.
func TestContextModeRealHook(t *testing.T) {
	root := os.Getenv("CMDHOOK_CONTEXT_MODE")
	if root == "" {
		t.Skip("set CMDHOOK_CONTEXT_MODE=/path/to/context-mode to run")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	b := mustLoad(t, Config{Root: root, TimeoutSec: 30})

	// Grep guidance is deterministic; use a fresh session dir/id so context-mode's
	// per-session dedup does not suppress a repeat run.
	cwd := t.TempDir()
	args := json.RawMessage(`{"pattern":"error"}`)
	out := b.run(context.Background(), "PreToolUse", "Grep", map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Grep",
		"tool_input":      args,
		"cwd":             cwd,
		"session_id":      "mow-test-" + filepath.Base(cwd),
	})
	if out.blocked {
		t.Fatalf("context-mode blocked a plain grep probe: %+v", out)
	}
	joined := strings.Join(out.contexts, "\n")
	if !strings.Contains(joined, "context_guidance") && !strings.Contains(joined, "ctx_") {
		t.Fatalf("expected context-mode guidance, got: %q", joined)
	}
	t.Logf("context-mode additionalContext delivered (%d bytes)", len(joined))
}
