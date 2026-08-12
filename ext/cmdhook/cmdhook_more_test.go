package cmdhook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

// scriptAt writes an executable sh script into root and returns its path.
func scriptAt(t *testing.T, root, name, body string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// --- load errors and parsing ---------------------------------------------

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	// Missing hooks file.
	if _, err := load(PluginConfig{Root: t.TempDir()}); err == nil {
		t.Fatal("expected error for missing hooks file")
	}
	// Invalid JSON.
	root := t.TempDir()
	writeHooksJSON(t, root, `{not json`)
	if _, err := load(PluginConfig{Root: root}); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadEmptyAndSkippedEntries(t *testing.T) {
	t.Parallel()
	// No hooks at all → nil bridge, no error.
	root := t.TempDir()
	writeHooksJSON(t, root, `{"hooks":{}}`)
	b, err := load(PluginConfig{Root: root})
	if err != nil || b != nil {
		t.Fatalf("want nil bridge, got %v err=%v", b, err)
	}

	// Entries with no usable commands are dropped entirely: bad matcher,
	// unknown type, and empty command all yield nothing.
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[
		{"matcher":"(","hooks":[{"type":"command","command":"echo hi"}]},
		{"matcher":"","hooks":[{"type":"intercept","command":"echo hi"}]},
		{"matcher":"","hooks":[{"type":"command","command":"   "}]}
	]}}`)
	b, err = load(PluginConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Fatalf("expected nil bridge when every entry is skipped, got %+v", b.events)
	}

	// A valid entry next to skipped ones still loads (bad matcher only skips
	// its own entry).
	good := scriptAt(t, root, "ok.sh", "true")
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[
		{"matcher":"(","hooks":[{"type":"command","command":"echo hi"}]},
		{"matcher":"","hooks":[{"type":"command","command":"`+good+`"}]}
	]}}`)
	b, err = load(PluginConfig{Root: root})
	if err != nil || b == nil {
		t.Fatalf("want loaded bridge, err=%v", err)
	}
	if len(b.events["PreToolUse"]) != 1 {
		t.Fatalf("entries: %+v", b.events["PreToolUse"])
	}
}

func TestLoadHooksFilePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	good := scriptAt(t, root, "ok.sh", "true")
	body := `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"` + good + `"}]}]}}`

	// Custom relative hooks_file.
	other := filepath.Join(root, "cfg", "my.json")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := load(PluginConfig{Root: root, HooksFile: "cfg/my.json"}); err != nil || b == nil {
		t.Fatalf("relative hooks_file: %v", err)
	}
	// Absolute hooks_file outside root.
	abs := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := load(PluginConfig{Root: root, HooksFile: abs}); err != nil || b == nil {
		t.Fatalf("absolute hooks_file: %v", err)
	}
}

func TestLoadTimeoutConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	good := scriptAt(t, root, "ok.sh", "true")
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"`+good+`"}]}]}}`)

	b, err := load(PluginConfig{Root: root})
	if err != nil || b == nil {
		t.Fatal(err)
	}
	if b.timeout.Seconds() != 10 {
		t.Fatalf("default timeout = %v", b.timeout)
	}
	b, err = load(PluginConfig{Root: root, TimeoutSec: 3})
	if err != nil || b == nil {
		t.Fatal(err)
	}
	if b.timeout.Seconds() != 3 {
		t.Fatalf("configured timeout = %v", b.timeout)
	}
}

// --- decision logic --------------------------------------------------------

// hookJSON builds a hooks.json with one matcher entry per PreToolUse case.
func oneEntry(matcher, cmd string) string {
	return `{"hooks":{"PreToolUse":[{"matcher":"` + matcher + `","hooks":[{"type":"command","command":"` + cmd + `"}]}]}}`
}

func TestRunDecisionVariants(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"hook_event_name": "PreToolUse"}

	tests := []struct {
		name       string
		script     string // sh body
		blocked    bool
		reasonHas  string
		contextHas string
	}{
		{
			name:      "decision-block-with-reason",
			script:    `echo '{"decision":"block","reason":"danger"}'`,
			blocked:   true,
			reasonHas: "danger",
		},
		{
			name:      "decision-block-default-reason",
			script:    `echo '{"decision":"block"}'`,
			blocked:   true,
			reasonHas: "blocked by cmdhook",
		},
		{
			name:      "continue-false-with-stopreason",
			script:    `echo '{"continue":false,"stopReason":"halt now"}'`,
			blocked:   true,
			reasonHas: "halt now",
		},
		{
			name:      "continue-false-default-reason",
			script:    `echo '{"continue":false}'`,
			blocked:   true,
			reasonHas: "stopped by cmdhook",
		},
		{
			name:      "permission-ask-denies",
			script:    `echo '{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"needs human"}}'`,
			blocked:   true,
			reasonHas: "needs human",
		},
		{
			name:      "permission-deny-default-reason",
			script:    `echo '{"hookSpecificOutput":{"permissionDecision":"deny"}}'`,
			blocked:   true,
			reasonHas: "denied by cmdhook",
		},
		{
			name:    "permission-allow-passes",
			script:  `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`,
			blocked: false,
		},
		{
			name:    "approve-decision-passes",
			script:  `echo '{"decision":"approve"}'`,
			blocked: false,
		},
		{
			name:    "continue-true-passes",
			script:  `echo '{"continue":true}'`,
			blocked: false,
		},
		{
			name:       "plain-text-stdout-is-context",
			script:     `echo "hello plain"`,
			blocked:    false,
			contextHas: "hello plain",
		},
		{
			name:    "empty-stdout-is-noop",
			script:  `true`,
			blocked: false,
		},
		{
			name:    "nonzero-exit-nonblocking",
			script:  `echo "boom" >&2; exit 1`,
			blocked: false,
		},
		{
			name:      "exit-2-empty-stderr",
			script:    `exit 2`,
			blocked:   true,
			reasonHas: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cmd := scriptAt(t, root, "h.sh", tc.script)
			writeHooksJSON(t, root, oneEntry("", cmd))
			b := mustLoad(t, Config{Root: root})
			out := b.run(ctx, "PreToolUse", "Bash", payload)
			if out.blocked != tc.blocked {
				t.Fatalf("blocked=%v want %v (%+v)", out.blocked, tc.blocked, out)
			}
			if tc.reasonHas != "" && !strings.Contains(out.reason, tc.reasonHas) {
				t.Fatalf("reason=%q want contains %q", out.reason, tc.reasonHas)
			}
			if tc.contextHas != "" {
				if len(out.contexts) != 1 || !strings.Contains(out.contexts[0], tc.contextHas) {
					t.Fatalf("contexts=%v", out.contexts)
				}
			}
		})
	}
}

func TestRunCompositionAndOrdering(t *testing.T) {
	root := t.TempDir()
	first := scriptAt(t, root, "first.sh", `echo '{"decision":"block","reason":"first reason"}'`)
	second := scriptAt(t, root, "second.sh", `echo '{"hookSpecificOutput":{"additionalContext":"extra"}}'`)
	third := scriptAt(t, root, "third.sh", `echo "plain"`)

	// Two commands in one entry: block wins, first reason kept; the second
	// still contributes context.
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[
		{"matcher":"","hooks":[
			{"type":"command","command":"`+first+`"},
			{"type":"command","command":"`+second+`"}
		]}
	]}}`)
	b := mustLoad(t, Config{Root: root})
	out := b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	if !out.blocked || out.reason != "first reason" {
		t.Fatalf("composition: %+v", out)
	}
	if len(out.contexts) != 1 || out.contexts[0] != "extra" {
		t.Fatalf("contexts: %+v", out)
	}

	// Two matching entries run in order; the first block reason sticks even
	// when a later entry blocks with another reason.
	a := scriptAt(t, root, "a.sh", `echo '{"decision":"block","reason":"reason-A"}'`)
	c := scriptAt(t, root, "c.sh", `echo "also blocked" >&2; exit 2`)
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[
		{"matcher":"","hooks":[{"type":"command","command":"`+a+`"}]},
		{"matcher":"Bash","hooks":[{"type":"command","command":"`+third+`"}]},
		{"matcher":"B.*","hooks":[{"type":"command","command":"`+c+`"}]}
	]}}`)
	b = mustLoad(t, Config{Root: root})
	out = b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	if !out.blocked || out.reason != "reason-A" {
		t.Fatalf("ordering: %+v", out)
	}
	if len(out.contexts) != 1 || out.contexts[0] != "plain" {
		t.Fatalf("matcher-mid contexts: %+v", out)
	}
}

func TestRunUpdatedInput(t *testing.T) {
	root := t.TempDir()
	cmd := scriptAt(t, root, "rewrite.sh",
		`echo '{"hookSpecificOutput":{"updatedInput":{"command":"safe -la","path":"/tmp"}}}'`)
	writeHooksJSON(t, root, oneEntry("Bash", cmd))
	b := mustLoad(t, Config{Root: root})
	out := b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	if out.blocked {
		t.Fatalf("unexpected block: %+v", out)
	}
	if len(out.updatedInput) == 0 || !strings.Contains(string(out.updatedInput), "safe -la") {
		t.Fatalf("updatedInput: %s", out.updatedInput)
	}
}

func TestRunMatcherSemantics(t *testing.T) {
	root := t.TempDir()
	hit := scriptAt(t, root, "hit.sh", `echo '{"decision":"block","reason":"hit"}'`)

	// Regex matcher only fires on matching tool names.
	writeHooksJSON(t, root, oneEntry("^(Bash|Edit)$", hit))
	b := mustLoad(t, Config{Root: root})
	if out := b.run(context.Background(), "PreToolUse", "Read", map[string]any{}); out.blocked {
		t.Fatalf("Read should not match: %+v", out)
	}
	if out := b.run(context.Background(), "PreToolUse", "Edit", map[string]any{}); !out.blocked {
		t.Fatalf("Edit should match: %+v", out)
	}

	// Non-tool events (matchName == "") run even with a non-empty matcher —
	// Claude treats the matcher as always-true there.
	writeHooksJSON(t, root, `{"hooks":{"UserPromptSubmit":[
		{"matcher":"Bash","hooks":[{"type":"command","command":"`+hit+`"}]}
	]}}`)
	b = mustLoad(t, Config{Root: root})
	if out := b.run(context.Background(), "UserPromptSubmit", "", map[string]any{}); !out.blocked {
		t.Fatalf("non-tool event with matcher should run: %+v", out)
	}
}

// --- timeout handling ------------------------------------------------------

func TestTimeouts(t *testing.T) {
	root := t.TempDir()
	slow := scriptAt(t, root, "slow.sh", `sleep 1.2`)
	// Per-command timeout overrides the (generous) global one.
	writeHooksJSON(t, root, `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[
		{"type":"command","command":"`+slow+`","timeout":1}
	]}]}}`)
	b := mustLoad(t, Config{Root: root, TimeoutSec: 30})
	out := b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	if out.blocked || len(out.contexts) != 0 {
		t.Fatalf("timed-out hook must be non-blocking: %+v", out)
	}

	// Global timeout applies when the command has none.
	writeHooksJSON(t, root, oneEntry("", slow))
	b = mustLoad(t, Config{Root: root, TimeoutSec: 1})
	out = b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	if out.blocked {
		t.Fatalf("global timeout should be non-blocking: %+v", out)
	}
}

// --- substitution and environment ------------------------------------------

func TestClaudePluginRootAndEnv(t *testing.T) {
	root := t.TempDir()
	// Both the literal ${CLAUDE_PLUGIN_ROOT} substitution and the env vars
	// must resolve to the plugin root / project dir.
	sh := scriptAt(t, root, "env.sh", `printf '%s|%s|%s' "${CLAUDE_PLUGIN_ROOT}" "$CLAUDE_PLUGIN_ROOT" "$CLAUDE_PROJECT_DIR"`)
	writeHooksJSON(t, root, oneEntry("", sh+" "+`'`+`dummy`+`'`)) // silence unused
	writeHooksJSON(t, root, oneEntry("", sh))
	b := mustLoad(t, Config{Root: root})
	out := b.run(context.Background(), "PreToolUse", "Bash", map[string]any{"cwd": root})
	if len(out.contexts) != 1 {
		t.Fatalf("contexts: %+v", out)
	}
	want := root + "|" + root + "|" + root
	if out.contexts[0] != want {
		t.Fatalf("env context = %q want %q", out.contexts[0], want)
	}

	// Command runs in payload cwd.
	marker := filepath.Join(root, "marker")
	cwdScript := scriptAt(t, root, "cwd.sh", `pwd > marker`)
	writeHooksJSON(t, root, oneEntry("", cwdScript))
	b = mustLoad(t, Config{Root: root})
	b.run(context.Background(), "PreToolUse", "Bash", map[string]any{"cwd": root})
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != root {
		t.Fatalf("cwd = %q want %q", strings.TrimSpace(string(raw)), root)
	}
}

// --- pure helpers ------------------------------------------------------------

func TestClaudeToolNameExtras(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"READ":         "Read", // case-insensitive
		"  bash ":      "Bash", // trimmed
		"mcp_":         "mcp__",
		"proc_start":   "proc_start", // not mcp_
		"weird-tool":   "weird-tool",
		"MCP_SRV_TOOL": "MCP_SRV_TOOL", // prefix match is case-sensitive
	} {
		if got := claudeToolName(in); got != want {
			t.Errorf("claudeToolName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRawOrEmpty(t *testing.T) {
	t.Parallel()
	if got := rawOrEmpty(nil); string(got) != `{}` {
		t.Fatalf("nil: %s", got)
	}
	if got := rawOrEmpty(json.RawMessage(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Fatalf("passthrough: %s", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Fatalf("got %q", got)
	}
	if got := firstNonEmpty("", " "); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("longer-than-five", 5); got != "longe…" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("exact", 5); got != "exact" {
		t.Fatalf("got %q", got)
	}
}

// --- basePayload --------------------------------------------------------------

func TestBasePayload(t *testing.T) {
	root := t.TempDir()
	scriptAt(t, root, "ok.sh", "true")
	writeHooksJSON(t, root, oneEntry("", filepath.Join(root, "ok.sh")))
	b := mustLoad(t, Config{Root: root})

	// Without an engine: hook_event_name + cwd fallback from os.Getwd.
	p := b.basePayload(context.Background(), "Stop")
	if p["hook_event_name"] != "Stop" {
		t.Fatalf("payload: %+v", p)
	}
	if _, ok := p["cwd"].(string); !ok {
		t.Fatalf("cwd missing: %+v", p)
	}

	// With an engine: cwd comes from the workspace.
	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{NoSession: true,
		Chat: func(ctx context.Context, m []mow.Message, tl []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	p = b.basePayload(mow.ContextWithEngine(context.Background(), eng), "PreToolUse")
	if p["cwd"] != eng.Workspace() {
		t.Fatalf("cwd=%v want %q", p["cwd"], eng.Workspace())
	}
}

// --- register: full hook wiring through ext --------------------------------

// resetHooks isolates ext's global hook registry for register tests.
func resetHooks(t *testing.T) {
	t.Helper()
	ext.Reset()
	t.Cleanup(ext.Reset)
}

func driveAllEvents(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()

	// PreTool: Bash is blocked via exit-2; Write gets args rewritten.
	for _, fn := range ext.PreToolHooks() {
		d, err := fn(ctx, ext.PreToolEvent{Name: "bash", Args: json.RawMessage(`{"command":"rm -rf /"}`)})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Deny || !strings.Contains(d.Message, "no rm") {
			t.Fatalf("PreTool Bash: %+v", d)
		}
	}
	for _, fn := range ext.PreToolHooks() {
		d, err := fn(ctx, ext.PreToolEvent{Name: "write", Args: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if d.Deny {
			t.Fatalf("Write should not be denied: %+v", d)
		}
		if !d.RewriteArgs || !strings.Contains(string(d.Args), "rewritten") {
			t.Fatalf("Write rewrite: %+v", d)
		}
	}

	// PostTool: blocked result gets annotated; context hook appends.
	for _, fn := range ext.PostToolHooks() {
		d, err := fn(ctx, ext.PostToolEvent{Name: "bash", Result: "output"})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Rewrite || !strings.Contains(d.Result, "[cmdhook] post nope") {
			t.Fatalf("PostTool block: %+v", d)
		}
		d, err = fn(ctx, ext.PostToolEvent{Name: "read", Result: "file text"})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Rewrite || !strings.Contains(d.Result, "file text") || !strings.Contains(d.Result, "post ctx") {
			t.Fatalf("PostTool context: %+v", d)
		}
		// Unmatched tool → no rewrite.
		d, err = fn(ctx, ext.PostToolEvent{Name: "glob", Result: "files"})
		if err != nil || d.Rewrite {
			t.Fatalf("PostTool unmatched: %+v err=%v", d, err)
		}
	}

	// UserPromptSubmit: blocked prompt returns an error.
	for _, fn := range ext.UserPromptHooks() {
		_, err := fn(ctx, ext.UserPromptEvent{Text: "evil"})
		if err == nil || !strings.Contains(err.Error(), "prompt blocked") {
			t.Fatalf("UserPrompt block: %v", err)
		}
	}

	// SessionStart: additionalContext becomes SystemAppend.
	for _, fn := range ext.SessionStartHooks() {
		d, err := fn(ctx, ext.SessionStartEvent{SessionID: "s1", Workspace: root})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(d.SystemAppend, "welcome") {
			t.Fatalf("SessionStart: %+v", d)
		}
	}

	// Stop: fire-and-forget; verify the command ran. Also an empty session id.
	stopMarker := filepath.Join(root, "stop-ran")
	for _, fn := range ext.StopHooks() {
		fn(ctx, ext.StopEvent{SessionID: "s1"})
		fn(ctx, ext.StopEvent{})
	}
	if _, err := os.Stat(stopMarker); err != nil {
		t.Fatalf("Stop hook did not run: %v", err)
	}

	// PreCompact: runs the command, returns an empty decision.
	compactMarker := filepath.Join(root, "compact-ran")
	for _, fn := range ext.PreCompactHooks() {
		d, err := fn(ctx, ext.PreCompactEvent{EstChars: 100, MaxChars: 1000, MessageCount: 5})
		if err != nil {
			t.Fatal(err)
		}
		_ = d
	}
	if _, err := os.Stat(compactMarker); err != nil {
		t.Fatalf("PreCompact hook did not run: %v", err)
	}
}

func TestRegisterAllEvents(t *testing.T) {
	resetHooks(t)
	root := t.TempDir()

	blockSh := scriptAt(t, root, "block.sh", `echo "no rm" >&2; exit 2`)
	rewriteSh := scriptAt(t, root, "rewrite.sh",
		`echo '{"hookSpecificOutput":{"updatedInput":{"content":"rewritten"}}}'`)
	postBlockSh := scriptAt(t, root, "postblock.sh", `echo "post nope" >&2; exit 2`)
	postCtxSh := scriptAt(t, root, "postctx.sh",
		`echo '{"hookSpecificOutput":{"additionalContext":"post ctx"}}'`)
	promptBlockSh := scriptAt(t, root, "promptblock.sh", `echo "no prompts" >&2; exit 2`)
	sessCtxSh := scriptAt(t, root, "sess.sh",
		`echo '{"hookSpecificOutput":{"additionalContext":"welcome"}}'`)
	stopSh := scriptAt(t, root, "stop.sh", `touch "`+filepath.Join(root, "stop-ran")+`"`)
	compactSh := scriptAt(t, root, "compact.sh", `touch "`+filepath.Join(root, "compact-ran")+`"`)

	writeHooksJSON(t, root, `{"hooks":{
		"PreToolUse":[
			{"matcher":"Bash","hooks":[{"type":"command","command":"`+blockSh+`"}]},
			{"matcher":"Write","hooks":[{"type":"command","command":"`+rewriteSh+`"}]}
		],
		"PostToolUse":[
			{"matcher":"Bash","hooks":[{"type":"command","command":"`+postBlockSh+`"}]},
			{"matcher":"Read","hooks":[{"type":"command","command":"`+postCtxSh+`"}]}
		],
		"UserPromptSubmit":[
			{"matcher":"","hooks":[{"type":"command","command":"`+promptBlockSh+`"}]}
		],
		"SessionStart":[
			{"matcher":"","hooks":[{"type":"command","command":"`+sessCtxSh+`"}]}
		],
		"Stop":[
			{"matcher":"","hooks":[{"type":"command","command":"`+stopSh+`"}]}
		],
		"PreCompact":[
			{"matcher":"","hooks":[{"type":"command","command":"`+compactSh+`"}]}
		]
	}}`)

	b := mustLoad(t, Config{Root: root})
	b.register()
	driveAllEvents(t, root)
}

func TestRegisterUserPromptAllow(t *testing.T) {
	resetHooks(t)
	root := t.TempDir()
	allowSh := scriptAt(t, root, "allow.sh",
		`echo '{"hookSpecificOutput":{"additionalContext":"be nice"}}'`)
	writeHooksJSON(t, root, `{"hooks":{"UserPromptSubmit":[
		{"matcher":"","hooks":[{"type":"command","command":"`+allowSh+`"}]}
	]}}`)
	b := mustLoad(t, Config{Root: root})
	b.register()
	if len(ext.UserPromptHooks()) != 1 {
		t.Fatalf("hooks = %d", len(ext.UserPromptHooks()))
	}
	// Non-blocking hook: contexts become SystemAppend.
	d, err := ext.UserPromptHooks()[0](context.Background(),
		ext.UserPromptEvent{Text: "hello", SessionID: "sess-9", Workspace: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.SystemAppend, "be nice") {
		t.Fatalf("SystemAppend: %+v", d)
	}
	// Without session/workspace fields the hook still runs.
	d, err = ext.UserPromptHooks()[0](context.Background(), ext.UserPromptEvent{Text: "hi"})
	if err != nil || !strings.Contains(d.SystemAppend, "be nice") {
		t.Fatalf("bare prompt: %+v err=%v", d, err)
	}
}

// --- setup: config discovery + registration gate ----------------------------

func setupReset(t *testing.T) {
	t.Helper()
	ext.Reset()
	t.Cleanup(ext.Reset)
}

func TestSetupFromConfigPaths(t *testing.T) {
	setupReset(t)
	t.Setenv("MOW_HOME", t.TempDir()) // keep extcfg's config.yaml fallback isolated
	root := t.TempDir()
	ok := scriptAt(t, root, "ok.sh", "true")
	writeHooksJSON(t, root, oneEntry("", ok))

	cfg := filepath.Join(t.TempDir(), "mow.yaml")
	if err := os.WriteFile(cfg, []byte("extensions:\n  cmdhook:\n    root: "+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(cfg); err != nil {
		t.Fatal(err)
	}
	if len(ext.PreToolHooks()) != 1 {
		t.Fatalf("PreTool hooks = %d want 1", len(ext.PreToolHooks()))
	}
	// Second call replaces (idempotent), does not accumulate.
	if err := setup(cfg); err != nil {
		t.Fatal(err)
	}
	if len(ext.PreToolHooks()) != 1 {
		t.Fatalf("replace registration: %d want 1", len(ext.PreToolHooks()))
	}
}

func TestSetupFallbackFile(t *testing.T) {
	setupReset(t)
	root := t.TempDir()
	ok := scriptAt(t, root, "ok.sh", "true")
	writeHooksJSON(t, root, oneEntry("", ok))
	t.Setenv("MOW_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(mow.Home(), "cmdhook.yaml"), []byte("root: "+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Home fallbacks only when the host path list includes $MOW_HOME/config.yaml
	// (LoadUserConfig). Empty path lists are hermetic and skip home files.
	if err := setup(filepath.Join(mow.Home(), "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if len(ext.PreToolHooks()) != 1 {
		t.Fatal("fallback cmdhook.yaml should register")
	}
}

func TestSetupNoConfig(t *testing.T) {
	setupReset(t)
	t.Setenv("MOW_HOME", t.TempDir())
	if err := setup(); err != nil {
		t.Fatal(err)
	}
	if n := len(ext.PreToolHooks()); n != 0 {
		t.Fatalf("no config should not register hooks, got %d", n)
	}
}

func TestSetupEmptyBridge(t *testing.T) {
	setupReset(t)
	t.Setenv("MOW_HOME", t.TempDir())
	// Root configured, hooks file present but empty → load returns nil,
	// setup stays inactive without error.
	root := t.TempDir()
	writeHooksJSON(t, root, `{"hooks":{}}`)
	cfg := filepath.Join(t.TempDir(), "mow.yaml")
	if err := os.WriteFile(cfg, []byte("extensions:\n  cmdhook:\n    root: "+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(cfg); err != nil {
		t.Fatal(err)
	}
	if n := len(ext.PreToolHooks()); n != 0 {
		t.Fatalf("empty bridge should not register hooks, got %d", n)
	}
}

func TestSetupReplacesAcrossConfigs(t *testing.T) {
	setupReset(t)
	t.Setenv("MOW_HOME", t.TempDir())
	rootA := t.TempDir()
	rootB := t.TempDir()
	writeHooksJSON(t, rootA, oneEntry("", scriptAt(t, rootA, "a.sh", "true")))
	writeHooksJSON(t, rootB, oneEntry("", scriptAt(t, rootB, "b.sh", "true")))
	cfgA := filepath.Join(t.TempDir(), "a.yaml")
	cfgB := filepath.Join(t.TempDir(), "b.yaml")
	if err := os.WriteFile(cfgA, []byte("extensions:\n  cmdhook:\n    root: "+rootA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgB, []byte("extensions:\n  cmdhook:\n    root: "+rootB+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(cfgA); err != nil {
		t.Fatal(err)
	}
	if err := setup(cfgB); err != nil {
		t.Fatal(err)
	}
	// Only one generation of hooks remains (B replaced A).
	if n := len(ext.PreToolHooks()); n != 1 {
		t.Fatalf("hooks after replace = %d want 1", n)
	}
}

func TestFailClosedTimeoutBlocks(t *testing.T) {
	root := t.TempDir()
	slow := scriptAt(t, root, "slow.sh", `sleep 5`)
	writeHooksJSON(t, root, oneEntry("", slow))
	b := mustLoad(t, Config{Root: root, TimeoutSec: 1, FailClosed: true})
	out := b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	if !out.blocked {
		t.Fatalf("fail_closed timeout must block: %+v", out)
	}
}

func TestSanitizeHookDiag(t *testing.T) {
	in := "api_key=supersecret token=abc Bearer xyz sk-abcdefghij"
	out := sanitizeHookDiag(in)
	if strings.Contains(out, "supersecret") || strings.Contains(out, "sk-abcdefghij") {
		t.Fatalf("leaked secret: %q", out)
	}
}

func TestSetupBadConfigSection(t *testing.T) {
	setupReset(t)
	t.Setenv("MOW_HOME", t.TempDir())
	// A cmdhook section that cannot decode into Config is a setup error.
	cfg := filepath.Join(t.TempDir(), "mow.yaml")
	if err := os.WriteFile(cfg, []byte("extensions:\n  cmdhook:\n    timeout_sec: [not-an-int]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(cfg); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestSetupErrors(t *testing.T) {
	setupReset(t)
	t.Setenv("MOW_HOME", t.TempDir())
	hostPaths := []string{filepath.Join(mow.Home(), "config.yaml")}

	// Malformed cmdhook.yaml.
	if err := os.WriteFile(filepath.Join(mow.Home(), "cmdhook.yaml"), []byte("root: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(hostPaths...); err == nil {
		t.Fatal("expected yaml error")
	}

	// Root configured but hooks file missing.
	if err := os.WriteFile(filepath.Join(mow.Home(), "cmdhook.yaml"), []byte("root: "+t.TempDir()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(hostPaths...); err == nil {
		t.Fatal("expected missing hooks file error")
	}

	// Empty root in fallback file → silently inactive.
	if err := os.WriteFile(filepath.Join(mow.Home(), "cmdhook.yaml"), []byte("root: \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup(hostPaths...); err != nil {
		t.Fatal(err)
	}
	if n := len(ext.PreToolHooks()); n != 0 {
		t.Fatalf("empty root should not register, hooks=%d", n)
	}
}
