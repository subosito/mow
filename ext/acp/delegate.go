package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/config"
)

// PeerSpec is one named peer under extensions.acp.peers.
//
// Type is the field set, not a separate key: `command` is an external ACP
// process; `model` is a native `mow acp --model …` peer. The two are exclusive.
// Shared: name, dir, timeout_sec.
type PeerSpec struct {
	// Name is the short id used in delegate args (e.g. "claude").
	Name string `yaml:"name" json:"name"`
	// Command is the peer argv that speaks ACP on stdio (external).
	Command []string `yaml:"command" json:"command,omitempty"`
	// Model selects a native mow peer (`mow acp --model`). Exclusive with Command.
	Model string `yaml:"model" json:"model,omitempty"`
	// Dir optional working directory (default: mow workspace).
	Dir string `yaml:"dir" json:"dir,omitempty"`
	// TimeoutSec caps one delegated prompt. 0 → 300 (external) or 600 (native).
	// On timeout the peer gets session/cancel then the process tree is dropped.
	TimeoutSec int `yaml:"timeout_sec" json:"timeout_sec,omitempty"`
	// Effort is native-only (--effort on mow acp). External peers put their
	// own flag in command; mow does not rewrite argv.
	Effort string `yaml:"effort" json:"effort,omitempty"`
	// PermissionMode is how the parent answers the peer's
	// session/request_permission: reject | allow. Omitted: native model:
	// peers allow (host already capped write/shell); external command:
	// peers reject. Legacy: argv containing --force implies allow when
	// this field is omitted.
	PermissionMode string `yaml:"permission_mode" json:"permission_mode,omitempty"`
	// Native-only. Nil inherits the host at delegate time (never exceeds host).
	AllowWrite *bool `yaml:"allow_write" json:"allow_write,omitempty"`
	AllowShell *bool `yaml:"allow_shell" json:"allow_shell,omitempty"`
	// Native-only. Nil inherits: true when the host denies both write and shell.
	ReadOnly *bool `yaml:"read_only" json:"read_only,omitempty"`
	// Native-only identity/role text prepended via --system-prefix.
	SystemPrefix string `yaml:"system_prefix" json:"system_prefix,omitempty"`
	// Native-only extra argv after the standard mow acp flags.
	ExtraArgs []string `yaml:"extra_args" json:"extra_args,omitempty"`
}

func (a PeerSpec) native() bool { return strings.TrimSpace(a.Model) != "" }

// Config is the extensions.acp section.
//
//	extensions:
//	  acp:
//	    peer_idle_sec: 900
//	    peers:
//	      - name: peer-agent
//	        command: [env, ANTHROPIC_MODEL=…, npx, -y, "@agentclientprotocol/claude-agent-acp"]
//	      - name: reviewer
//	        model: gpt-5-mini
//	        effort: high
type Config struct {
	// PeerIdleSec drops unused peer processes after this many seconds.
	// 0 or omitted → default 900. -1 → never idle-evict (still drop if !Alive()).
	PeerIdleSec int `yaml:"peer_idle_sec"`
	// Peers are named entries for the delegate tool (external command or native model).
	Peers []PeerSpec `yaml:"peers"`
}

const (
	// ToolName is the model-facing tool. ACP is the current peer protocol.
	ToolName = "delegate"
)

// sharedDelegate is the singleton delegate tool so packs (e.g. ops) can
// merge peers without replacing each other.
var (
	sharedMu       sync.Mutex
	sharedDelegate *delegateTool
	sharedGen      int
	orphanedByGen  = map[int][]*delegateTool{}
)

func init() {
	ext.RegisterGenerationRelease(releaseSharedPeers)
	// Drop pooled peers when the host Prompt finishes so cursor-agent does
	// not sit on the tty until quit or the 15m idle timer.
	ext.RegisterStopSource("acp", func(context.Context, ext.StopEvent) {
		dropSharedPeers()
	})
}

// RegisterFromConfig loads config (same path list as mow.New / BeforeNew) and
// registers delegate when peers is non-empty.
// Must run *before* mow.New so the tool is in the registry.
//
// Each call builds a fresh tool from the effective config (replace, not merge)
// so a profile's extensions.acp fully replaces a previous Engine's peers and
// does not accumulate into a process-global agent map. Prefer
// RegisterFromEngine after New when engine-scoped isolation is required
// (concurrent Engines in one process).
func RegisterFromConfig(configPaths ...string) error {
	// Path-only load: defaults + the handed paths + env. No implicit
	// $MOW_HOME/config.yaml — the host (engine.New with LoadUserConfig) must
	// include that path when it wants user/global extensions.acp.
	cfg, err := config.LoadPaths(configPaths...)
	if err != nil {
		return err
	}
	var c Config
	if err := cfg.Extension("acp", &c); err != nil {
		return err
	}
	agents, err := resolvePeers(c)
	if err != nil {
		return err
	}
	return replaceSharedAgents(agents, cfg.Workspace, c.PeerIdleSec)
}

// RegisterFromEngine is like RegisterFromConfig using an already-built engine's
// extension section. The tool is engine-scoped (AddTool) so concurrent Engines
// do not share peer state. Prefer this when an Engine already exists; use
// RegisterFromConfig only from BeforeNew path-only hooks.
func RegisterFromEngine(eng *mow.Engine) error {
	if eng == nil {
		return fmt.Errorf("acp: nil engine")
	}
	var c Config
	if err := eng.Extension("acp", &c); err != nil {
		return err
	}
	agents, err := resolvePeers(c)
	if err != nil {
		return err
	}
	indexed := indexPeers(agents)
	if len(indexed) == 0 {
		return nil
	}
	tool := newDelegateTool(indexed, eng.Workspace(), peerIdleDuration(c.PeerIdleSec))
	if err := eng.AddTool(tool); err != nil {
		return err
	}
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventRunEnd {
			tool.dropPeers()
		}
	})
	eng.RegisterCleanup(func() { tool.closeAll() })
	return nil
}

func dropSharedPeers() {
	sharedMu.Lock()
	t := sharedDelegate
	sharedMu.Unlock()
	if t != nil {
		t.dropPeers()
	}
}

// replaceSharedAgents installs a new process-global delegate tool from the
// given agent list (full replace). Used by RegisterFromConfig so each
// BeforeNew reflects only the current effective config.
func replaceSharedAgents(agents []PeerSpec, workspace string, peerIdleSec int) error {
	indexed := indexPeers(agents)
	sharedMu.Lock()
	old := sharedDelegate
	oldGen := sharedGen
	if len(indexed) == 0 {
		sharedDelegate = nil
		sharedGen = 0
		sharedMu.Unlock()
		retireShared(old, oldGen)
		return nil
	}
	sharedDelegate = newDelegateTool(indexed, strings.TrimSpace(workspace), peerIdleDuration(peerIdleSec))
	sharedGen = ext.BeforeNewGeneration()
	sharedMu.Unlock()
	retireShared(old, oldGen)
	ext.RegisterTool(sharedDelegate)
	return nil
}

func retireShared(t *delegateTool, gen int) {
	if t == nil {
		return
	}
	if gen > 0 && ext.GenerationEngineRefs(gen) > 0 {
		sharedMu.Lock()
		orphanedByGen[gen] = append(orphanedByGen[gen], t)
		sharedMu.Unlock()
		return
	}
	t.closeAll()
}

// releaseSharedPeers kills leftover delegate subprocesses when the last
// Engine for a BeforeNew generation closes (mow acp/run exit). Idle
// eviction only runs on the next tool call, so without this a peer can
// reparent to PID 1 and live for hours.
func releaseSharedPeers(gen int) {
	if gen <= 0 {
		return
	}
	sharedMu.Lock()
	var toClose []*delegateTool
	if sharedGen == gen && sharedDelegate != nil {
		toClose = append(toClose, sharedDelegate)
	}
	if orph := orphanedByGen[gen]; len(orph) > 0 {
		toClose = append(toClose, orph...)
		delete(orphanedByGen, gen)
	}
	sharedMu.Unlock()
	for _, t := range toClose {
		t.closeAll()
	}
}

// AppendPeers merges peer specs into delegate (creating the tool if needed).
// Used by packs (e.g. ops profiles) that add peers on top of an existing
// registration. Empty list is a no-op. peerIdleSec: 0 = default, -1 = no idle
// drop; only applied when the shared tool is first created.
//
// Prefer RegisterFromConfig / RegisterFromEngine for full config-driven
// registration — those replace rather than accumulate.
func AppendPeers(agents []PeerSpec, workspace string, peerIdleSec int) {
	indexed := indexPeers(agents)
	if len(indexed) == 0 {
		return
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedDelegate == nil {
		sharedDelegate = newDelegateTool(map[string]PeerSpec{}, strings.TrimSpace(workspace), peerIdleDuration(peerIdleSec))
		sharedGen = ext.BeforeNewGeneration()
	} else if sharedDelegate.workspace == "" && strings.TrimSpace(workspace) != "" {
		sharedDelegate.workspace = strings.TrimSpace(workspace)
	}
	for k, v := range indexed {
		sharedDelegate.agents[k] = v
	}
	ext.RegisterTool(sharedDelegate)
}

func peerIdleDuration(sec int) time.Duration {
	if sec < 0 {
		return 0 // disabled
	}
	if sec == 0 {
		return 15 * time.Minute
	}
	return time.Duration(sec) * time.Second
}

func indexPeers(list []PeerSpec) map[string]PeerSpec {
	m := map[string]PeerSpec{}
	for _, a := range list {
		name := strings.ToLower(strings.TrimSpace(a.Name))
		if name == "" || (len(a.Command) == 0 && !a.native()) {
			continue
		}
		if a.TimeoutSec <= 0 {
			if a.native() {
				a.TimeoutSec = 600
			} else {
				a.TimeoutSec = 300
			}
		}
		m[name] = a
	}
	return m
}

// peerSlot holds a long-lived ACP client + session for reuse across tool calls.
type peerSlot struct {
	// mu serializes use of the peer (a peer is a single stdio conversation):
	// held for the whole delegate call, covering OnChunk set/clear and Prompt.
	mu        sync.Mutex
	client    *Client
	sessionID string
	dir       string
	// key is the full pool key (agent+dir+command fingerprint) for dropPeer.
	key string
	// lastUsed is guarded by delegateTool.peersMu.
	lastUsed time.Time
	// starting is non-nil while the peer is being spawned (reserved slot);
	// closed when the spawn finishes. Guarded by delegateTool.peersMu.
	starting chan struct{}
}

type delegateTool struct {
	agents    map[string]PeerSpec
	workspace string
	peerIdle  time.Duration // 0 = no idle eviction

	peersMu sync.Mutex
	peers   map[string]*peerSlot // key: agent\x00dir

	reaperMu   sync.Mutex
	reaperStop chan struct{}
	reaperOn   bool
	closed     bool
}

func newDelegateTool(agents map[string]PeerSpec, workspace string, peerIdle time.Duration) *delegateTool {
	if agents == nil {
		agents = map[string]PeerSpec{}
	}
	t := &delegateTool{
		agents:    agents,
		workspace: workspace,
		peerIdle:  peerIdle,
		peers:     map[string]*peerSlot{},
	}
	t.ensureReaper()
	return t
}

// ensureReaper ticks idle eviction even when no further delegate call
// arrives. Without it, a peer lives until Engine.Close (or the next
// getOrStart), which is how cursor-agent stays up after one task.
func (t *delegateTool) ensureReaper() {
	if t == nil || t.peerIdle <= 0 {
		return
	}
	t.reaperMu.Lock()
	defer t.reaperMu.Unlock()
	if t.closed || t.reaperOn {
		return
	}
	t.reaperStop = make(chan struct{})
	t.reaperOn = true
	stop := t.reaperStop
	idle := t.peerIdle
	go func() {
		d := idle
		if d > 15*time.Second {
			d = 15 * time.Second
		}
		tick := time.NewTicker(d)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				t.peersMu.Lock()
				t.evictIdleLocked(now)
				t.peersMu.Unlock()
			}
		}
	}()
}

func (t *delegateTool) stopReaper() {
	if t == nil {
		return
	}
	t.reaperMu.Lock()
	defer t.reaperMu.Unlock()
	t.closed = true
	if t.reaperOn && t.reaperStop != nil {
		close(t.reaperStop)
		t.reaperOn = false
		t.reaperStop = nil
	}
}

func (t *delegateTool) Name() string    { return ToolName }
func (t *delegateTool) Untrusted() bool { return true }
func (t *delegateTool) Description() string {
	names := make([]string, 0, len(t.agents))
	for n := range t.agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return "Delegate a task to a named peer agent over ACP (in other harnesses often called a subagent). " +
		"Only call this when the user names a peer or explicitly asks to delegate — not because a task list looks parallelizable. " +
		"Peers are configured under extensions.acp.peers (command = external ACP peer; model = native mow acp). " +
		"Process/session is reused across calls when possible. " +
		"Long runs are capped by the agent's timeout_sec; cancel the host turn to abort. " +
		"Args: agent (one of: " + strings.Join(names, ", ") + "), prompt (required), cwd (optional absolute or workspace-relative). " +
		"Alias: subagent is accepted as a synonym for agent."
}
func (t *delegateTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string","description":"Named agent id (alias: subagent)"},"subagent":{"type":"string","description":"Synonym for agent (other harnesses use this term)"},"prompt":{"type":"string"},"cwd":{"type":"string"}},"required":["prompt"]}`)
}

// peerKey identifies a reusable peer process. It must include the effective
// argv and permission mode so a policy/model/cwd change never reuses a stale
// process started under a different command (e.g. host gained write, model
// switch, extra roots changed).
func peerKey(agent, dir string, cmd []string, permMode string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(agent)))
	b.WriteByte(0)
	b.WriteString(dir)
	b.WriteByte(0)
	b.WriteString(normalizePermissionMode(permMode))
	for _, a := range cmd {
		b.WriteByte(0)
		b.WriteString(a)
	}
	return b.String()
}

func (t *delegateTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Agent    string `json:"agent"`
		Subagent string `json:"subagent"` // synonym for agent (other harnesses)
		Prompt   string `json:"prompt"`
		Cwd      string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	agentName := strings.TrimSpace(a.Agent)
	if agentName == "" {
		agentName = strings.TrimSpace(a.Subagent)
	}
	if agentName == "" {
		return "", fmt.Errorf("delegate: agent (or subagent) is required")
	}
	spec, ok := t.agents[strings.ToLower(agentName)]
	if !ok {
		return "", fmt.Errorf("delegate: unknown agent %q (configure extensions.acp.peers; \"subagent\" means the same thing)", agentName)
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("delegate: empty prompt")
	}
	dir := strings.TrimSpace(a.Cwd)
	if dir == "" {
		dir = strings.TrimSpace(spec.Dir)
	}
	if dir == "" {
		dir = t.workspace
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(t.workspace, dir)
	}
	// Apply the running Engine's complete FS jail, not only the delegate
	// registry's workspace. This is what makes --extra-root usable as an ACP
	// cwd (for example, delegating from mow into an allowed sibling mowi repo).
	// The fallback preserves registration-only tests/callers without an Engine
	// in context. Relative cwd remains workspace-relative in both paths.
	if eng := mow.EngineFromContext(ctx); eng != nil {
		resolved, err := eng.ResolvePath(dir)
		if err != nil {
			return "", fmt.Errorf("delegate: cwd %q escapes path jail: %w", dir, err)
		}
		dir = resolved
	} else if t.workspace != "" {
		resolved, err := resolveInWorkspace(t.workspace, dir)
		if err != nil {
			return "", fmt.Errorf("delegate: cwd %q escapes workspace", dir)
		}
		dir = resolved
	}

	to := time.Duration(spec.TimeoutSec) * time.Second
	pctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	host := hostPolicyFromContext(ctx, t.workspace)
	cmd := peerCommand(spec, host, dir)
	perm := effectivePermissionMode(spec)
	key := peerKey(spec.Name, dir, cmd, perm)
	slot, err := t.getOrStart(pctx, spec, dir, host, cmd, perm, key)
	if err != nil {
		return "", err
	}

	// One delegate call at a time per peer: a peer is a single stdio
	// conversation, so OnChunk and the reply accumulator must not be shared.
	slot.mu.Lock()
	defer slot.mu.Unlock()

	agentName = spec.Name
	// Immediate UI signal so hosts show "claude: prompt running…" during TTFT.
	if eng := mow.EngineFromContext(ctx); eng != nil {
		eng.Emit(mow.Event{
			Type:  mow.EventDelegateProgress,
			Agent: agentName,
			Tool:  "prompt",
			Delta: "running…",
		})
	}
	slot.client.SetOnChunk(func(delta string) {
		if eng := mow.EngineFromContext(ctx); eng != nil {
			eng.Emit(mow.Event{
				Type:  mow.EventDelegateChunk,
				Agent: agentName,
				Delta: delta,
			})
		}
	})
	slot.client.SetOnProgress(func(kind, text string) {
		if eng := mow.EngineFromContext(ctx); eng != nil {
			eng.Emit(mow.Event{
				Type:  mow.EventDelegateProgress,
				Agent: agentName,
				Tool:  kind,
				Delta: clipProgressText(text),
			})
		}
	})
	// Peer write/edit → host harness.tool.start/end so the parent transcript
	// paints the same path row / diff card as a local tool. Not an Exec.
	startedMut := map[string]bool{}
	slot.client.SetOnFileMutation(func(m fileMutation) {
		if eng := mow.EngineFromContext(ctx); eng != nil {
			emitHostFileMutation(eng, agentName, m, startedMut)
		}
	})
	defer slot.client.SetOnChunk(nil)
	defer slot.client.SetOnProgress(nil)
	defer slot.client.SetOnFileMutation(nil)

	reply, stop, usage, err := slot.client.Prompt(pctx, slot.sessionID, prompt)
	t.peersMu.Lock()
	slot.lastUsed = time.Now()
	t.peersMu.Unlock()
	if err != nil {
		alive := slot.client.Alive()
		// Cancel/timeout/dead peer or hard transport failure: drop so the next
		// call does not reuse a half-dead stdio session.
		drop := !alive || pctx.Err() != nil ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			strings.Contains(err.Error(), "peer process exited")
		if drop {
			t.dropPeer(key, slot)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(pctx.Err(), context.DeadlineExceeded) {
			return "", delegatePromptError(spec.Name, spec.TimeoutSec, alive, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(pctx.Err(), context.Canceled) {
			return "", fmt.Errorf("delegate: agent %q cancelled (host turn aborted; session/cancel sent)", spec.Name)
		}
		return "", err
	}
	// Surface the peer's provider-reported usage so hosts can show true spend
	// including native mow peers (external agents may omit usage → zeros).
	if eng := mow.EngineFromContext(ctx); eng != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		eng.Emit(mow.Event{
			Type:         mow.EventDelegateUsage,
			Agent:        agentName,
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
		})
	}
	if strings.TrimSpace(reply) == "" {
		reply = "(peer returned no agent_message_chunk text; stopReason=" + stop + ")"
	}
	return formatDelegateResult(spec.Name, stop, reply), nil
}

// defaultDelegateSummaryChars caps the parent-visible peer body. Full reply is
// still streamed to the host via harness.delegate.chunk; the tool result keeps
// a compact summary so a deep peer dive does not bloat parent context.
const defaultDelegateSummaryChars = 4000

func formatDelegateResult(agentName, stop, reply string) string {
	body := strings.TrimRight(reply, " \t\r\n")
	runess := []rune(body)
	if len(runess) > defaultDelegateSummaryChars {
		body = string(runess[:defaultDelegateSummaryChars]) + "\n…(peer reply summarized for parent context; full stream was emitted live)"
	}
	if sum := extractPeerSummary(reply); sum != "" {
		return fmt.Sprintf("agent: %s\nstop: %s\n\n## Peer summary\n%s", agentName, stop, sum)
	}
	return fmt.Sprintf("agent: %s\nstop: %s\n\n%s", agentName, stop, body)
}

func extractPeerSummary(reply string) string {
	lines := strings.Split(reply, "\n")
	start := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		low := strings.ToLower(trim)
		if strings.HasPrefix(low, "## summary") || low == "summary:" || strings.HasPrefix(low, "summary:") {
			start = i
		}
	}
	if start < 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range lines[start:] {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := strings.TrimSpace(b.String())
	runess := []rune(out)
	if len(runess) > defaultDelegateSummaryChars {
		out = string(runess[:defaultDelegateSummaryChars]) + "\n…(truncated)"
	}
	return out
}

func delegatePromptError(agent string, timeoutSec int, alive bool, err error) error {
	msg := fmt.Sprintf("delegate: agent %q timed out after %ds (session/cancel sent; peer alive=%v)", agent, timeoutSec, alive)
	if err != nil && err.Error() != "" {
		msg += ": " + err.Error()
	}
	return errors.New(msg)
}

func (t *delegateTool) getOrStart(ctx context.Context, spec PeerSpec, dir string, host *hostPeerPolicy, cmd []string, perm, key string) (*peerSlot, error) {
	t.ensureReaper()
	if key == "" {
		key = peerKey(spec.Name, dir, cmd, perm)
	}
	for {
		t.peersMu.Lock()
		if t.peers == nil {
			t.peers = map[string]*peerSlot{}
		}
		t.evictIdleLocked(time.Now())

		slot := t.peers[key]
		if slot != nil && slot.starting != nil {
			// Another caller is spawning this peer: wait outside the lock.
			starting := slot.starting
			t.peersMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-starting:
			}
			continue // re-check: slot was published or removed
		}
		if slot != nil && slot.client != nil && slot.client.Alive() && slot.sessionID != "" {
			slot.lastUsed = time.Now()
			t.peersMu.Unlock()
			return slot, nil
		}
		// Dead or missing — reserve the key, then spawn without holding
		// peersMu so a slow peer start does not stall other delegations.
		if slot != nil {
			delete(t.peers, key)
			if slot.client != nil && slot.mu.TryLock() {
				_ = slot.client.Close()
				slot.mu.Unlock()
			} // busy dead slot: its user drops it when Prompt errors
		}
		res := &peerSlot{dir: dir, key: key, starting: make(chan struct{})}
		t.peers[key] = res
		t.peersMu.Unlock()

		cl := &Client{
			Command:        append([]string(nil), cmd...),
			Dir:            dir,
			PermissionMode: perm,
		}
		sid, err := cl.Start(ctx)

		t.peersMu.Lock()
		done := res.starting
		res.starting = nil
		if err != nil {
			if t.peers[key] == res {
				delete(t.peers, key)
			}
			close(done)
			t.peersMu.Unlock()
			return nil, err
		}
		res.client = cl
		res.sessionID = sid
		res.lastUsed = time.Now()
		close(done)
		t.peersMu.Unlock()
		return res, nil
	}
}

// evictIdleLocked drops peers idle longer than peerIdle, or not Alive().
// Caller holds peersMu. Slots mid-spawn or in use by a delegate call are skipped.
func (t *delegateTool) evictIdleLocked(now time.Time) {
	for k, slot := range t.peers {
		if slot == nil {
			delete(t.peers, k)
			continue
		}
		if slot.starting != nil {
			continue // reserved, spawn in flight
		}
		if slot.client == nil {
			delete(t.peers, k)
			continue
		}
		if !slot.mu.TryLock() {
			continue // in use by a delegate call
		}
		dead := !slot.client.Alive()
		idle := t.peerIdle > 0 && !slot.lastUsed.IsZero() && now.Sub(slot.lastUsed) > t.peerIdle
		if dead || idle {
			_ = slot.client.Close()
			delete(t.peers, k)
		}
		slot.mu.Unlock()
	}
}

// dropPeer removes slot (the exact instance the caller used) and closes its
// client. The map entry is only deleted if it still points at that instance.
func (t *delegateTool) dropPeer(key string, slot *peerSlot) {
	if slot == nil {
		return
	}
	t.peersMu.Lock()
	if cur, ok := t.peers[key]; ok && cur == slot {
		delete(t.peers, key)
	}
	t.peersMu.Unlock()
	if slot.client != nil {
		_ = slot.client.Close()
	}
}

// dropPeers kills every pooled peer but keeps the tool (next Prompt can
// spawn again). Does not wait for slot.mu — Prompt-end must not stall.
func (t *delegateTool) dropPeers() {
	if t == nil {
		return
	}
	t.peersMu.Lock()
	slots := make([]*peerSlot, 0, len(t.peers))
	for k, slot := range t.peers {
		delete(t.peers, k)
		slots = append(slots, slot)
	}
	t.peersMu.Unlock()
	for _, slot := range slots {
		if slot == nil || slot.client == nil {
			continue
		}
		_ = slot.client.Close()
	}
}

// closeAll drops every pooled peer and stops the idle reaper (Engine.Close).
func (t *delegateTool) closeAll() {
	if t == nil {
		return
	}
	t.stopReaper()
	t.dropPeers()
}
