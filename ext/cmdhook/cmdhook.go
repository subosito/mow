// Package cmdhook bridges Claude Code-style command hooks into mow's hook
// system. A hooks.json (the Claude Code plugin schema) declares commands per
// event; cmdhook executes matching commands with the same contract those
// plugins already speak: the event as JSON on stdin, an optional decision as
// JSON on stdout, exit code 2 = block with stderr as the reason.
//
// Config (extensions.cmdhook, or $MOW_HOME/cmdhook.yaml):
//
//	extensions:
//	  cmdhook:
//	    root: /path/to/plugin        # ${CLAUDE_PLUGIN_ROOT} substitution
//	    hooks_file: hooks/hooks.json # default, relative to root
//	    timeout_sec: 10              # per command (default 10)
//
// Supported events: PreToolUse, PostToolUse, UserPromptSubmit, SessionStart,
// Stop, PreCompact. Tool names are translated to Claude conventions for
// matchers and payloads (read → Read, mcp_srv_x → mcp__srv_x). A PreToolUse
// permissionDecision of "ask" is treated as deny — mow's engine has no
// interactive prompt; hosts with approval UIs gate power tools themselves.
package cmdhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/packcfg"
)

// Config is extensions.cmdhook.
type Config struct {
	Root       string `yaml:"root"`
	HooksFile  string `yaml:"hooks_file"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

// hooksFile is the Claude Code plugin hooks.json schema (subset).
type hooksFile struct {
	Hooks map[string][]matcherEntry `json:"hooks"`
}

type matcherEntry struct {
	Matcher string     `json:"matcher"`
	Hooks   []cmdEntry `json:"hooks"`
}

type cmdEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"` // seconds; overrides Config.TimeoutSec
}

// hookOut is the Claude Code hook stdout schema (subset).
type hookOut struct {
	Decision           string `json:"decision"` // "block" | "approve"
	Reason             string `json:"reason"`
	Continue           *bool  `json:"continue"`
	StopReason         string `json:"stopReason"`
	HookSpecificOutput struct {
		AdditionalContext        string          `json:"additionalContext"`
		PermissionDecision       string          `json:"permissionDecision"` // allow | deny | ask
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
}

type compiled struct {
	re   *regexp.Regexp // nil = match all
	cmds []cmdEntry
}

type bridge struct {
	root    string
	timeout time.Duration
	events  map[string][]compiled
}

var (
	mu         sync.Mutex
	registered bool
)

func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return setup(configPaths...)
	})
}

func setup(configPaths ...string) error {
	mu.Lock()
	defer mu.Unlock()
	if registered {
		return nil
	}
	var c Config
	ok, err := packcfg.DecodeSection("cmdhook", configPaths, &c)
	if err != nil {
		return fmt.Errorf("cmdhook extensions: %w", err)
	}
	if !ok || strings.TrimSpace(c.Root) == "" {
		// fallback file, mirroring mcp.yaml / lsp.yaml
		raw, rerr := os.ReadFile(filepath.Join(mow.Home(), "cmdhook.yaml"))
		if rerr != nil {
			return nil
		}
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("cmdhook: cmdhook.yaml: %w", err)
		}
	}
	if strings.TrimSpace(c.Root) == "" {
		return nil
	}
	b, err := load(c)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	b.register()
	registered = true
	return nil
}

func load(c Config) (*bridge, error) {
	root, err := filepath.Abs(strings.TrimSpace(c.Root))
	if err != nil {
		return nil, fmt.Errorf("cmdhook: root: %w", err)
	}
	hf := strings.TrimSpace(c.HooksFile)
	if hf == "" {
		hf = filepath.Join("hooks", "hooks.json")
	}
	if !filepath.IsAbs(hf) {
		hf = filepath.Join(root, hf)
	}
	raw, err := os.ReadFile(hf)
	if err != nil {
		return nil, fmt.Errorf("cmdhook: %s: %w", hf, err)
	}
	var file hooksFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("cmdhook: %s: %w", hf, err)
	}
	timeout := time.Duration(c.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	b := &bridge{root: root, timeout: timeout, events: map[string][]compiled{}}
	n := 0
	for event, entries := range file.Hooks {
		for _, me := range entries {
			var re *regexp.Regexp
			if m := strings.TrimSpace(me.Matcher); m != "" {
				re, err = regexp.Compile(m)
				if err != nil {
					slog.Warn("cmdhook: bad matcher skipped", "event", event, "matcher", me.Matcher, "err", err)
					continue
				}
			}
			var cmds []cmdEntry
			for _, ce := range me.Hooks {
				if ce.Type != "" && ce.Type != "command" {
					continue
				}
				if strings.TrimSpace(ce.Command) == "" {
					continue
				}
				cmds = append(cmds, ce)
				n++
			}
			if len(cmds) > 0 {
				b.events[event] = append(b.events[event], compiled{re: re, cmds: cmds})
			}
		}
	}
	if n == 0 {
		return nil, nil
	}
	fmt.Fprintf(os.Stderr, "cmdhook: registered %d command hook(s) from %q\n", n, root)
	return b, nil
}

// outcome aggregates one event's command results.
type outcome struct {
	blocked      bool
	reason       string
	contexts     []string
	updatedInput json.RawMessage
}

func (b *bridge) run(ctx context.Context, event, matchName string, payload map[string]any) outcome {
	var out outcome
	entries := b.events[event]
	for _, ent := range entries {
		if ent.re != nil && matchName != "" && !ent.re.MatchString(matchName) {
			continue
		}
		if ent.re != nil && matchName == "" && ent.re.String() != "" {
			// Non-tool events with a non-empty matcher: Claude treats the
			// matcher as always-true there; mirror that.
			_ = ent
		}
		for _, ce := range ent.cmds {
			ho, blocked, reason, ok := b.execOne(ctx, ce, payload)
			if !ok {
				continue
			}
			if blocked {
				out.blocked = true
				if out.reason == "" {
					out.reason = reason
				}
				continue
			}
			hs := ho.HookSpecificOutput
			switch hs.PermissionDecision {
			case "deny", "ask": // no interactive prompt at engine level
				out.blocked = true
				if out.reason == "" {
					out.reason = firstNonEmpty(hs.PermissionDecisionReason, "denied by cmdhook")
				}
			}
			if ho.Decision == "block" {
				out.blocked = true
				if out.reason == "" {
					out.reason = firstNonEmpty(ho.Reason, "blocked by cmdhook")
				}
			}
			if ho.Continue != nil && !*ho.Continue {
				out.blocked = true
				if out.reason == "" {
					out.reason = firstNonEmpty(ho.StopReason, "stopped by cmdhook")
				}
			}
			if s := strings.TrimSpace(hs.AdditionalContext); s != "" {
				out.contexts = append(out.contexts, s)
			}
			if len(hs.UpdatedInput) > 0 {
				out.updatedInput = hs.UpdatedInput
			}
		}
	}
	return out
}

// execOne runs a single command hook. ok=false means the run failed
// non-blockingly (timeout, bad JSON, nonzero-but-not-2 exit) and was logged.
func (b *bridge) execOne(ctx context.Context, ce cmdEntry, payload map[string]any) (ho hookOut, blocked bool, reason string, ok bool) {
	timeout := b.timeout
	if ce.Timeout > 0 {
		timeout = time.Duration(ce.Timeout) * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdStr := strings.ReplaceAll(ce.Command, "${CLAUDE_PLUGIN_ROOT}", b.root)
	cmd := exec.CommandContext(tctx, "sh", "-c", cmdStr)
	cwd, _ := payload["cwd"].(string)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(),
		"CLAUDE_PLUGIN_ROOT="+b.root,
		"CLAUDE_PROJECT_DIR="+cwd,
	)
	in, err := json.Marshal(payload)
	if err != nil {
		return ho, false, "", false
	}
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		if ee, isExit := runErr.(*exec.ExitError); isExit && ee.ExitCode() == 2 {
			// Claude contract: exit 2 blocks; stderr is the reason for the model.
			return ho, true, strings.TrimSpace(stderr.String()), true
		}
		slog.Warn("cmdhook: hook failed (non-blocking)",
			"command", firstNonEmpty(truncate(cmdStr, 80), cmdStr), "err", runErr,
			"stderr", truncate(stderr.String(), 200))
		return ho, false, "", false
	}
	body := bytes.TrimSpace(stdout.Bytes())
	if len(body) == 0 {
		return ho, false, "", true
	}
	if err := json.Unmarshal(body, &ho); err != nil {
		// Plain-text stdout on some events is additional context in Claude;
		// treat it the same way.
		ho.HookSpecificOutput.AdditionalContext = string(body)
	}
	return ho, false, "", true
}

func (b *bridge) register() {
	if len(b.events["PreToolUse"]) > 0 {
		ext.RegisterPreTool(func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
			payload := b.basePayload(ctx, "PreToolUse")
			payload["tool_name"] = claudeToolName(e.Name)
			payload["tool_input"] = rawOrEmpty(e.Args)
			out := b.run(ctx, "PreToolUse", claudeToolName(e.Name), payload)
			d := ext.PreToolDecision{
				Deny:              out.blocked,
				Message:           out.reason,
				AdditionalContext: strings.Join(out.contexts, "\n\n"),
			}
			if !out.blocked && len(out.updatedInput) > 0 {
				d.Args = out.updatedInput
				d.RewriteArgs = true
			}
			return d, nil
		})
	}
	if len(b.events["PostToolUse"]) > 0 {
		ext.RegisterPostTool(func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
			payload := b.basePayload(ctx, "PostToolUse")
			payload["tool_name"] = claudeToolName(e.Name)
			payload["tool_input"] = rawOrEmpty(e.Args)
			payload["tool_response"] = e.Result
			out := b.run(ctx, "PostToolUse", claudeToolName(e.Name), payload)
			if out.blocked {
				return ext.PostToolDecision{
					Result:  e.Result + "\n\n[cmdhook] " + out.reason,
					Rewrite: true,
				}, nil
			}
			if len(out.contexts) > 0 {
				return ext.PostToolDecision{
					Result:  e.Result + "\n\n" + strings.Join(out.contexts, "\n\n"),
					Rewrite: true,
				}, nil
			}
			return ext.PostToolDecision{}, nil
		})
	}
	if len(b.events["UserPromptSubmit"]) > 0 {
		ext.RegisterUserPrompt(func(ctx context.Context, e ext.UserPromptEvent) (ext.UserPromptDecision, error) {
			payload := b.basePayload(ctx, "UserPromptSubmit")
			payload["prompt"] = e.Text
			if e.SessionID != "" {
				payload["session_id"] = e.SessionID
			}
			if e.Workspace != "" {
				payload["cwd"] = e.Workspace
			}
			out := b.run(ctx, "UserPromptSubmit", "", payload)
			if out.blocked {
				return ext.UserPromptDecision{}, fmt.Errorf("cmdhook: prompt blocked: %s", out.reason)
			}
			return ext.UserPromptDecision{SystemAppend: strings.Join(out.contexts, "\n\n")}, nil
		})
	}
	if len(b.events["SessionStart"]) > 0 {
		ext.RegisterSessionStart(func(ctx context.Context, e ext.SessionStartEvent) (ext.SessionStartDecision, error) {
			payload := map[string]any{
				"hook_event_name": "SessionStart",
				"session_id":      e.SessionID,
				"cwd":             e.Workspace,
				"source":          "startup",
			}
			out := b.run(ctx, "SessionStart", "", payload)
			return ext.SessionStartDecision{SystemAppend: strings.Join(out.contexts, "\n\n")}, nil
		})
	}
	if len(b.events["Stop"]) > 0 {
		ext.RegisterStop(func(ctx context.Context, e ext.StopEvent) {
			payload := b.basePayload(ctx, "Stop")
			payload["stop_hook_active"] = false
			if e.SessionID != "" {
				payload["session_id"] = e.SessionID
			}
			_ = b.run(ctx, "Stop", "", payload)
		})
	}
	if len(b.events["PreCompact"]) > 0 {
		ext.RegisterPreCompact(func(ctx context.Context, e ext.PreCompactEvent) (ext.PreCompactDecision, error) {
			payload := b.basePayload(ctx, "PreCompact")
			payload["trigger"] = "auto"
			_ = b.run(ctx, "PreCompact", "", payload)
			return ext.PreCompactDecision{}, nil
		})
	}
}

// basePayload fills the fields every Claude hook event carries. Engine
// context supplies cwd/session when the hook fires inside a Prompt.
func (b *bridge) basePayload(ctx context.Context, event string) map[string]any {
	p := map[string]any{"hook_event_name": event}
	if eng := mow.EngineFromContext(ctx); eng != nil {
		p["cwd"] = eng.Workspace()
		p["session_id"] = eng.SessionID()
	}
	if _, ok := p["cwd"]; !ok {
		if wd, err := os.Getwd(); err == nil {
			p["cwd"] = wd
		}
	}
	return p
}

// claudeToolName maps mow tool names onto Claude Code conventions so
// existing matchers ("Bash|Read|…", "mcp__") work unchanged.
func claudeToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return "Read"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "write":
		return "Write"
	case "edit":
		return "Edit"
	case "bash":
		return "Bash"
	}
	if rest, ok := strings.CutPrefix(name, "mcp_"); ok {
		return "mcp__" + rest
	}
	return name
}

func rawOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
