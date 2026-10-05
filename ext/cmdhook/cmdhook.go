// Package cmdhook bridges Claude Code-style command hooks into mow's hook
// system. Host-owned Agent Plugins ($MOW_HOME/plugins and workspace profile
// plugins/) that ship hooks/hooks.json register automatically. There is no
// extensions.cmdhook section: install the plugin, keep ext/cmdhook linked.
//
// A hooks.json (the Claude Code / Agent Plugins schema) declares commands per
// event; cmdhook executes matching commands with the same contract those
// plugins already speak: the event as JSON on stdin, an optional decision as
// JSON on stdout, exit code 2 = block with stderr as the reason.
//
// Supported events: PreToolUse, PostToolUse, UserPromptSubmit, SessionStart,
// Stop, PreCompact. A Stop hook's decision "block" (with reason) or
// additionalContext keeps the agent going, capped at 8 continuations in a row.
// Plain (non-JSON) stdout is model context only on UserPromptSubmit and
// SessionStart. permissionDecision "defer" denies: mow cannot resume deferred
// calls. Tool names are translated to Claude conventions for
// matchers and payloads (read → Read, mcp_srv_x → mcp__srv_x). A PreToolUse
// permissionDecision of "ask" is treated as deny — mow's engine has no
// interactive prompt; hosts with approval UIs gate power tools themselves.
package cmdhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/extcfg"
	toolssandbox "github.com/subosito/mow/internal/sandbox"
)

// PluginConfig is one hooks.json plugin instance (tests and plugin discovery).
type PluginConfig struct {
	Name       string
	Root       string
	HooksFile  string
	TimeoutSec int
	MinTurns   int
	// FailClosed: when true, hook timeout/failure blocks the tool/prompt
	// (same as exit code 2). Default false = fail-open (warn only).
	FailClosed bool
}

// hookSource is the ext.ClearHookSource / Register*Source id for this extension.
const hookSource = "cmdhook"

// maxHookIOBytes caps each of stdout/stderr retained from a hook subprocess.
// Excess is discarded so a runaway plugin cannot bloat the agent context or logs.
const maxHookIOBytes = 64 << 10

// hostPluginHooks lists hooks.json from host-owned Agent Plugins.
// Hermetic BeforeNew (no $MOW_HOME/config.yaml on the path list) sees none.
func hostPluginHooks(configPaths []string) []PluginConfig {
	if !extcfg.IncludesUserConfig(configPaths) {
		return nil
	}
	var out []PluginConfig
	roots := mow.HostOwnedPluginRoots(mow.Home(), configPaths)
	for _, info := range mow.ListPlugins(roots) {
		if strings.TrimSpace(info.HooksFile) == "" {
			continue
		}
		out = append(out, PluginConfig{
			Name:      info.ID,
			Root:      info.Path,
			HooksFile: info.HooksFile,
		})
	}
	return out
}

type hooksFile struct {
	Hooks map[string][]matcherEntry `json:"hooks"`
	// Modules lists Claude Code mod entry points (JS). mow does not run
	// them; they are only counted so the skip is reported, not silent.
	Modules []string `json:"modules"`
}

// supportedEventList is the Claude hook events cmdhook bridges.
var supportedEventList = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "SessionStart", "Stop", "StopFailure", "PreCompact"}

var supportedEvents = func() map[string]bool {
	m := map[string]bool{}
	for _, e := range supportedEventList {
		m[e] = true
	}
	return m
}()

type matcherEntry struct {
	Matcher string     `json:"matcher"`
	Hooks   []cmdEntry `json:"hooks"`
}

type cmdEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"` // seconds; overrides PluginConfig.TimeoutSec
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
	name           string
	root           string
	timeout        time.Duration
	minTurns       int
	failClosed     bool
	events         map[string][]compiled
	mu             sync.Mutex
	sessionStarted bool
}

func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return setup(configPaths...)
	})
}

// setup re-registers cmdhook from host-owned Agent Plugins every BeforeNew.
// Prior registrations are cleared first so profile B cannot inherit profile A
// hooks, and hermetic engines do not keep host plugins after a later host New.
func setup(configPaths ...string) error {
	ext.ClearHookSource(hookSource)
	ext.ClearExtensionKind("cmdhook")

	plugins := hostPluginHooks(configPaths)
	for _, p := range plugins {
		b, err := load(p)
		if err != nil {
			return err
		}
		if b != nil {
			b.register()
		}
	}
	return nil
}

func load(p PluginConfig) (*bridge, error) {
	root, err := filepath.Abs(strings.TrimSpace(p.Root))
	if err != nil {
		return nil, fmt.Errorf("cmdhook: root: %w", err)
	}
	hf := strings.TrimSpace(p.HooksFile)
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
	// 0 = per-event defaults (defaultEventTimeout); a plugin-wide value
	// overrides them, and an entry's own timeout overrides both.
	timeout := max(time.Duration(p.TimeoutSec)*time.Second, 0)
	b := &bridge{
		name: p.Name, root: root, timeout: timeout, minTurns: p.MinTurns,
		failClosed: p.FailClosed, events: map[string][]compiled{},
	}
	n := 0
	skipped := map[string]int{} // unsupported handler types, by type
	var unsupported []string
	for event := range file.Hooks {
		if !supportedEvents[event] {
			unsupported = append(unsupported, event)
		}
	}
	for event, entries := range file.Hooks {
		if !supportedEvents[event] {
			continue
		}
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
					skipped[ce.Type]++
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
	if len(skipped) > 0 || len(unsupported) > 0 || (len(file.Hooks) == 0 && len(file.Modules) > 0) {
		slices.Sort(unsupported)
		types := slices.Sorted(maps.Keys(skipped))
		fmt.Fprintf(os.Stderr, "cmdhook: %q: skipped unsupported hooks (handler types %v, events %v, js modules %d); mow runs command hooks for %v\n",
			root, types, unsupported, len(file.Modules), supportedEventList)
	}
	if n == 0 {
		return nil, nil
	}
	fmt.Fprintf(os.Stderr, "cmdhook: registered %d command hook(s) from %q\n", n, root)
	return b, nil
}

// defaultEventTimeout mirrors Claude Code's hook defaults: 600 s on most
// events, 30 s on UserPromptSubmit (it blocks every prompt), and a short
// budget for SessionEnd-like teardown. PreToolUse is 60 s (D-41): a gate that
// stalls should not hold a tool call for ten minutes; a timeout there is
// "no decision" unless the plugin is fail_closed.
func defaultEventTimeout(event string) time.Duration {
	switch event {
	case "UserPromptSubmit":
		return 30 * time.Second
	case "PreToolUse":
		return 60 * time.Second
	case "Stop":
		return 60 * time.Second
	default:
		return 600 * time.Second
	}
}

func (b *bridge) timeoutFor(event string) time.Duration {
	if b.timeout > 0 {
		return b.timeout
	}
	return defaultEventTimeout(event)
}

// maxContextChars caps each additionalContext a hook returns (Claude Code:
// 10,000 characters per field). Longer text keeps head and tail.
const maxContextChars = 10_000

func capContext(s string) string {
	r := []rune(s)
	if len(r) <= maxContextChars {
		return s
	}
	head, tail := maxContextChars*3/4, maxContextChars/4
	return string(r[:head]) + fmt.Sprintf("\n…(hook context: %d chars elided)…\n", len(r)-head-tail) + string(r[len(r)-tail:])
}

// outcome aggregates one event's command results.
type outcome struct {
	blocked      bool
	halt         bool // continue:false — stop the run, not just this call
	reason       string
	contexts     []string
	updatedInput json.RawMessage
}

func (b *bridge) run(ctx context.Context, event, matchName string, payload map[string]any) outcome {
	var out outcome
	// Already cancelled: do not start subprocesses that can only fail. Stop
	// hooks in particular fire during teardown, when ctx is usually dead.
	if ctx.Err() != nil {
		return out
	}
	entries := b.events[event]
	for _, ent := range entries {
		if ent.re != nil && matchName != "" && !ent.re.MatchString(matchName) {
			continue
		}
		// Non-tool events (empty matchName) ignore the matcher entirely:
		// Claude treats it as always-true there, so the entry still runs.
		for _, ce := range ent.cmds {
			if ctx.Err() != nil {
				return out
			}
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
			case "defer":
				// Claude pauses the session so an outside process can resume
				// the call later. mow has no deferred resume, so the call must
				// not run now: deny it and say why.
				out.blocked = true
				if out.reason == "" {
					out.reason = firstNonEmpty(hs.PermissionDecisionReason, "deferred by hook") + " (mow cannot resume deferred tool calls)"
				}
			}
			if ho.Decision == "block" {
				out.blocked = true
				if out.reason == "" {
					out.reason = firstNonEmpty(ho.Reason, "blocked by cmdhook")
				}
			}
			if ho.Continue != nil && !*ho.Continue {
				out.halt = true
				out.blocked = true
				if out.reason == "" {
					out.reason = firstNonEmpty(ho.StopReason, "stopped by cmdhook")
				}
			}
			if s := strings.TrimSpace(hs.AdditionalContext); s != "" {
				s = capContext(normalizeClaudeToolNames(s, b.name))
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
// non-blockingly (timeout, bad JSON, nonzero-but-not-2 exit) and was logged,
// unless failClosed turns the failure into a block.
func (b *bridge) execOne(ctx context.Context, ce cmdEntry, payload map[string]any) (ho hookOut, blocked bool, reason string, ok bool) {
	event, _ := payload["hook_event_name"].(string)
	timeout := b.timeoutFor(event)
	if ce.Timeout > 0 {
		timeout = time.Duration(ce.Timeout) * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// ${CLAUDE_PLUGIN_ROOT} is exported in cmd.Env below; the shell expands
	// it. Splicing the path into the command text left it unquoted.
	cmd := exec.CommandContext(tctx, "sh", "-c", ce.Command)
	// Own process group, and kill the whole group on cancel/timeout.
	//
	// CommandContext only signals the direct child (`sh`). A hook that spawns
	// anything — and `sh -c` almost always does — leaves grandchildren holding
	// the inherited stdout/stderr pipes, and cmd.Run blocks until those close.
	// Ctrl+C would then appear to hang for the hook's full remaining runtime
	// instead of returning immediately. WaitDelay bounds that wait even if a
	// process survives the signal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	cwd, _ := payload["cwd"].(string)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(hookEnv(ctx),
		"CLAUDE_PLUGIN_ROOT="+b.root,
		"CLAUDE_PROJECT_DIR="+cwd,
	)
	if strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) == "" {
		if home := strings.TrimSpace(mow.Home()); home != "" {
			cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+home)
		}
	}
	in, err := json.Marshal(payload)
	if err != nil {
		// The hook never saw the event, so it could not evaluate it. A gate
		// must not be skipped silently: block under fail_closed, warn otherwise.
		if b.failClosed {
			return ho, true, "cmdhook: event could not be encoded for the hook", true
		}
		slog.Warn("cmdhook: event could not be encoded; hook skipped", "err", err)
		return ho, false, "", false
	}
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr cappedBuffer
	stdout.max = maxHookIOBytes
	stderr.max = maxHookIOBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	stderrText := sanitizeHookDiag(strings.TrimSpace(stderr.String()))
	if runErr != nil {
		if ee, isExit := runErr.(*exec.ExitError); isExit && ee.ExitCode() == 2 {
			// Claude contract: exit 2 blocks; stderr is the reason for the model.
			return ho, true, firstNonEmpty(stderrText, "blocked by cmdhook"), true
		}
		// Cancellation is not a hook failure. On Ctrl+C the whole context is
		// torn down and every in-flight hook dies with "context canceled" —
		// warning about it is noise at the exact moment the user asked to
		// stop, and in the TUI (which keeps Warn+) it paints over the
		// alt-screen. Stay silent and let the caller unwind.
		//
		// Distinguish the caller's cancel from this hook's own timeout:
		// a hook that exceeded its budget is a real problem worth reporting,
		// so only ctx.Err() (not tctx.Err()) suppresses the warning.
		if ctx.Err() != nil || errors.Is(runErr, context.Canceled) {
			return ho, false, "", false
		}
		cmdLabel := sanitizeHookDiag(firstNonEmpty(truncate(ce.Command, 80), ce.Command))
		if errors.Is(runErr, context.DeadlineExceeded) || tctx.Err() != nil {
			msg := "cmdhook: hook timed out; no decision"
			if b.failClosed {
				return ho, true, firstNonEmpty(stderrText, "cmdhook timed out"), true
			}
			slog.Warn(msg+" (non-blocking)",
				"command", cmdLabel,
				"timeout", timeout,
				"stderr", truncate(stderrText, 200))
			return ho, false, "", false
		}
		// Claude contract: on any other exit, a valid JSON object on stdout
		// still decides (standard decision events). Only without one is the
		// exit a failure.
		if body := bytes.TrimSpace(stdout.Bytes()); len(body) > 0 && body[0] == '{' && json.Unmarshal(body, &ho) == nil {
			return ho, false, "", true
		}
		if b.failClosed {
			return ho, true, firstNonEmpty(stderrText, "cmdhook failed: "+runErr.Error()), true
		}
		slog.Warn("cmdhook: hook failed (non-blocking)",
			"command", cmdLabel, "err", runErr,
			"stderr", truncate(stderrText, 200))
		return ho, false, "", false
	}
	body := bytes.TrimSpace(stdout.Bytes())
	if len(body) == 0 {
		return ho, false, "", true
	}
	if err := json.Unmarshal(body, &ho); err != nil {
		// Plain-text stdout reaches the model only on the events where Claude
		// Code adds it as context. Elsewhere (tool events, Stop, PreCompact)
		// it is debug output: injecting it would let any hook that prints
		// put text into tool results.
		event, _ := payload["hook_event_name"].(string)
		if plainTextIsContext(event) {
			ho.HookSpecificOutput.AdditionalContext = string(body)
		} else {
			slog.Debug("cmdhook: plain stdout ignored", "event", event, "bytes", len(body))
		}
	}
	return ho, false, "", true
}

// plainTextIsContext reports whether non-JSON stdout is model context for
// event (Claude Code: UserPromptSubmit and SessionStart among the events
// cmdhook emits).
func plainTextIsContext(event string) bool {
	return event == "UserPromptSubmit" || event == "SessionStart"
}

// cappedBuffer retains at most max bytes while still accepting all writes so
// the child never blocks on a full pipe.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.max <= 0 {
		return len(p), nil
	}
	remain := c.max - c.buf.Len()
	if remain > 0 {
		if len(p) > remain {
			_, _ = c.buf.Write(p[:remain])
		} else {
			_, _ = c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte  { return c.buf.Bytes() }
func (c *cappedBuffer) String() string { return c.buf.String() }

// sanitizeHookDiag redacts common secret shapes from diagnostic log lines.
func sanitizeHookDiag(s string) string {
	if s == "" {
		return s
	}
	// Bearer first: the key=value rule would otherwise eat only "Bearer"
	// from "Authorization: Bearer <token>" and leave the token on the line.
	s = redactBearer(s)
	// Key=value / key: value for common secret names.
	for _, key := range []string{"api_key", "apikey", "token", "secret", "password", "authorization", "bearer"} {
		s = redactKeyValue(s, key)
	}
	// sk-… style tokens
	if i := strings.Index(s, "sk-"); i >= 0 {
		j := i + 3
		for j < len(s) && isSecretRune(s[j]) {
			j++
		}
		if j-i > 8 {
			s = s[:i] + "sk-[redacted]" + s[j:]
		}
	}
	return s
}

func isSecretRune(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_'
}

// redactBearer replaces the token after every "Bearer <ws>" (RFC 6750 b64token
// alphabet) with [redacted].
func redactBearer(s string) string {
	const key = "bearer"
	lower := strings.ToLower(s)
	pos := 0
	for pos < len(lower) {
		i := strings.Index(lower[pos:], key)
		if i < 0 {
			break
		}
		i += pos
		j := i + len(key)
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		k := j
		for k < len(s) && (isSecretRune(s[k]) || strings.IndexByte("._~+/=", s[k]) >= 0) {
			k++
		}
		if j == i+len(key) || k == j {
			pos = i + len(key) // "bearer=" / bare word: leave to redactKeyValue
			continue
		}
		s = s[:j] + "[redacted]" + s[k:]
		lower = strings.ToLower(s)
		pos = j + len("[redacted]")
	}
	return s
}

func redactKeyValue(s, key string) string {
	lower := strings.ToLower(s)
	k := strings.ToLower(key)
	pos := 0
	for pos < len(lower) {
		i := strings.Index(lower[pos:], k)
		if i < 0 {
			break
		}
		i += pos
		j := i + len(k)
		for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '"' || s[j] == '\'') {
			j++
		}
		if j >= len(s) || (s[j] != '=' && s[j] != ':') {
			pos = i + 1
			continue
		}
		j++
		for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '"' || s[j] == '\'') {
			j++
		}
		k2 := j
		for k2 < len(s) && !strings.ContainsRune("&\r\n,}\"' \t", rune(s[k2])) {
			k2++
		}
		s = s[:j] + "[redacted]" + s[k2:]
		lower = strings.ToLower(s)
		pos = j + len("[redacted]")
	}
	return s
}

func (b *bridge) runSessionStart(ctx context.Context) string {
	return b.runSessionStartSource(ctx, "startup")
}

// runSessionStartSource fires SessionStart with Claude's source value:
// "startup" for a new session, "resume" when prior history was loaded.
func (b *bridge) runSessionStartSource(ctx context.Context, source string) string {
	b.mu.Lock()
	b.sessionStarted = true
	b.mu.Unlock()

	if len(b.events["SessionStart"]) == 0 {
		return ""
	}
	payload := b.basePayload(ctx, "SessionStart")
	payload["source"] = source
	if m, _ := payload["model"].(string); m == "" {
		if eng := mow.EngineFromContext(ctx); eng != nil {
			payload["model"] = eng.Model()
		}
	}
	out := b.run(ctx, "SessionStart", "", payload)
	return strings.Join(out.contexts, "\n\n")
}

func (b *bridge) runSessionStartIfNeeded(ctx context.Context) string {
	b.mu.Lock()
	already := b.sessionStarted
	b.mu.Unlock()
	if already {
		return ""
	}
	return b.runSessionStart(ctx)
}

func normalizeClaudeToolNames(text, pluginName string) string {
	if text == "" {
		return text
	}
	replacements := []string{
		"mcp__plugin_" + pluginName + "_" + pluginName + "__", "mcp_" + pluginName + "_",
		"mcp__" + pluginName + "__", "mcp_" + pluginName + "_",
		"mcp__" + pluginName + "_", "mcp_" + pluginName + "_",
	}
	res := text
	for i := 0; i < len(replacements); i += 2 {
		res = strings.ReplaceAll(res, replacements[i], replacements[i+1])
	}
	return res
}

func (b *bridge) register() {
	ext.RegisterExtensionInstance("cmdhook", b.name, b.minTurns)
	target := "cmdhook:" + b.name

	if len(b.events["PreToolUse"]) > 0 {
		ext.RegisterPreToolSource(hookSource, func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
			if !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.PreToolDecision{}, nil
			}
			payload := b.basePayload(ctx, "PreToolUse")
			payload["tool_name"] = claudeToolName(e.Name)
			payload["tool_input"] = rawOrEmpty(e.Args)
			if e.ToolCallID != "" {
				payload["tool_use_id"] = e.ToolCallID
			}
			out := b.run(ctx, "PreToolUse", claudeToolName(e.Name), payload)
			d := ext.PreToolDecision{
				Deny:              out.blocked,
				Halt:              out.halt,
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
	if len(b.events["PostToolUseFailure"]) > 0 {
		// Claude: fires instead of PostToolUse when the tool failed; output
		// may add context next to the error.
		ext.RegisterPostToolSource(hookSource, func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
			if e.ExecErr == nil || e.Denied || !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.PostToolDecision{}, nil
			}
			payload := b.basePayload(ctx, "PostToolUseFailure")
			payload["tool_name"] = claudeToolName(e.Name)
			payload["tool_input"] = rawOrEmpty(e.Args)
			payload["error"] = e.ExecErr.Error()
			payload["is_interrupt"] = errors.Is(e.ExecErr, context.Canceled)
			if e.ToolCallID != "" {
				payload["tool_use_id"] = e.ToolCallID
			}
			if e.Duration > 0 {
				payload["duration_ms"] = e.Duration.Milliseconds()
			}
			out := b.run(ctx, "PostToolUseFailure", claudeToolName(e.Name), payload)
			if len(out.contexts) > 0 {
				return ext.PostToolDecision{Result: e.Result + "\n\n" + strings.Join(out.contexts, "\n\n"), Rewrite: true}, nil
			}
			return ext.PostToolDecision{}, nil
		})
	}
	if len(b.events["PostToolUse"]) > 0 {
		ext.RegisterPostToolSource(hookSource, func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
			// Failed calls go to PostToolUseFailure (Claude fires one or the other).
			if (e.ExecErr != nil && !e.Denied) || !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.PostToolDecision{}, nil
			}
			payload := b.basePayload(ctx, "PostToolUse")
			payload["tool_name"] = claudeToolName(e.Name)
			payload["tool_input"] = rawOrEmpty(e.Args)
			if e.ToolCallID != "" {
				payload["tool_use_id"] = e.ToolCallID
			}
			if e.Duration > 0 {
				payload["duration_ms"] = e.Duration.Milliseconds()
			}
			// Bound tool_response before shipping it to an untrusted shell hook.
			payload["tool_response"] = truncate(e.Result, maxHookIOBytes)
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
		ext.RegisterUserPromptSource(hookSource, func(ctx context.Context, e ext.UserPromptEvent) (ext.UserPromptDecision, error) {
			if !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.UserPromptDecision{}, nil
			}
			sysStart := b.runSessionStartIfNeeded(ctx)
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
			appends := out.contexts
			if sysStart != "" {
				appends = append([]string{sysStart}, appends...)
			}
			return ext.UserPromptDecision{SystemAppend: strings.Join(appends, "\n\n")}, nil
		})
	}
	if len(b.events["SessionStart"]) > 0 {
		ext.RegisterSessionStartSource(hookSource, func(ctx context.Context, e ext.SessionStartEvent) (ext.SessionStartDecision, error) {
			if !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.SessionStartDecision{}, nil
			}
			source := "startup"
			if e.Resumed {
				source = "resume"
			}
			sys := b.runSessionStartSource(ctx, source)
			return ext.SessionStartDecision{SystemAppend: sys}, nil
		})
	}
	if len(b.events["Stop"]) > 0 {
		// Claude's Stop contract: decision "block" (with a reason) or
		// additionalContext keeps the agent going. The loop caps consecutive
		// continuations (agent.MaxFinishContinuations), mirroring Claude's 8.
		ext.RegisterBeforeFinishSource(hookSource, func(ctx context.Context, e ext.BeforeFinishEvent) (ext.BeforeFinishDecision, error) {
			if !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.BeforeFinishDecision{}, nil
			}
			payload := b.basePayload(ctx, "Stop")
			payload["stop_hook_active"] = e.Continuations > 0
			payload["last_assistant_message"] = e.AssistantText
			out := b.run(ctx, "Stop", "", payload)
			var parts []string
			if out.blocked && strings.TrimSpace(out.reason) != "" {
				parts = append(parts, "Stop hook feedback: "+out.reason)
			}
			parts = append(parts, out.contexts...)
			return ext.BeforeFinishDecision{Continue: strings.Join(parts, "\n\n")}, nil
		})
		// A Run that ends with an error never reaches the before-finish
		// seam; fire Stop for it here (observe-only) so the hook still runs
		// once per prompt. Successful runs were handled above.
		ext.RegisterStopSource(hookSource, func(ctx context.Context, e ext.StopEvent) {
			// Claude fires StopFailure instead of Stop when the turn ends on
			// an API error; plugins that declare it get that one.
			if e.Err == nil || len(b.events["StopFailure"]) > 0 || !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return
			}
			payload := b.basePayload(ctx, "Stop")
			payload["stop_hook_active"] = false
			if e.SessionID != "" {
				payload["session_id"] = e.SessionID
			}
			_ = b.run(ctx, "Stop", "", payload)
		})
	}
	if len(b.events["StopFailure"]) > 0 {
		// Claude: runs instead of Stop when the turn ends on an error; output
		// and exit code are ignored.
		ext.RegisterStopSource(hookSource, func(ctx context.Context, e ext.StopEvent) {
			if e.Err == nil || errors.Is(e.Err, context.Canceled) || !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return
			}
			payload := b.basePayload(ctx, "StopFailure")
			payload["error"] = "unknown"
			payload["error_details"] = e.Err.Error()
			if e.Text != "" {
				payload["last_assistant_message"] = e.Text
			}
			if e.SessionID != "" {
				payload["session_id"] = e.SessionID
			}
			_ = b.run(context.WithoutCancel(ctx), "StopFailure", "", payload)
		})
	}
	if len(b.events["PreCompact"]) > 0 {
		ext.RegisterPreCompactSource(hookSource, func(ctx context.Context, e ext.PreCompactEvent) (ext.PreCompactDecision, error) {
			if !ext.IsExtensionActive(target, ext.TurnFromContext(ctx)) {
				return ext.PreCompactDecision{}, nil
			}
			payload := b.basePayload(ctx, "PreCompact")
			payload["trigger"] = "auto"
			payload["est_chars"] = e.EstChars
			payload["max_chars"] = e.MaxChars
			payload["message_count"] = e.MessageCount
			payload["custom_instructions"] = nil
			out := b.run(ctx, "PreCompact", "", payload)
			// Claude: decision "block" (or exit 2) stops compaction. Only the
			// loop's automatic path fires PreCompact, so trigger is "auto".
			if out.blocked {
				slog.Info("cmdhook: compaction skipped by PreCompact hook", "plugin", b.name, "reason", out.reason)
				return ext.PreCompactDecision{Skip: true}, nil
			}
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

// hookEnv is the environment a hook subprocess may inherit: the parent
// environment without credentials, scrubbed by the same rule as the shell
// tools (sandbox.ScrubEnvWith). When the hook fires inside a Prompt the engine
// supplies the extra exact deny list (its configured api_key_env names); the
// shape rule still applies outside a Prompt.
func hookEnv(ctx context.Context) []string {
	if eng := mow.EngineFromContext(ctx); eng != nil {
		return eng.ShellEnv()
	}
	return toolssandbox.ScrubEnv(os.Environ())
}

// firstNonEmpty keeps the value untrimmed: hook stderr is shown verbatim.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// truncate clamps s to n bytes, cutting on a rune boundary. Callers pass hook
// subprocess output, so a naive s[:n] can split a rune and log invalid UTF-8.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
