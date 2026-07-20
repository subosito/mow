package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/config"
)

// AgentSpec is one peer harness reachable via ACP (extensions.acp.agents).
type AgentSpec struct {
	// Name is the short id used in acp_delegate args (e.g. "claude").
	Name string `yaml:"name" json:"name"`
	// Command is the peer argv that speaks ACP on stdio.
	Command []string `yaml:"command" json:"command"`
	// Dir optional working directory (default: mow workspace).
	Dir string `yaml:"dir" json:"dir"`
	// TimeoutSec caps one delegated prompt (default 600).
	TimeoutSec int `yaml:"timeout_sec" json:"timeout_sec"`
}

// Config is the extensions.acp section.
//
//	extensions:
//	  acp:
//	    peer_idle_sec: 900   # drop idle peers (default 900; -1 = never by idle)
//	    agents:
//	      - name: peer
//	        command: [peer-agent, --acp]
type Config struct {
	// PeerIdleSec drops unused peer processes after this many seconds.
	// 0 or omitted → default 900. -1 → never idle-evict (still drop if !Alive()).
	PeerIdleSec int         `yaml:"peer_idle_sec"`
	Agents      []AgentSpec `yaml:"agents"`
}

// RegisterFromConfig loads config (same paths as mow.New) and registers
// acp_delegate when extensions.acp.agents is non-empty.
// Must run *before* mow.New so the tool is in the registry.
func RegisterFromConfig(configPaths ...string) error {
	cfg, err := config.Load(configPaths...)
	if err != nil {
		return err
	}
	var c Config
	if err := cfg.Extension("acp", &c); err != nil {
		return err
	}
	agents := indexAgents(c.Agents)
	if len(agents) == 0 {
		return nil
	}
	ext.RegisterTool(&delegateTool{
		agents:    agents,
		workspace: cfg.Workspace,
		peerIdle:  peerIdleDuration(c.PeerIdleSec),
		peers:     map[string]*peerSlot{},
	})
	return nil
}

// RegisterFromEngine is like RegisterFromConfig using an already-built engine's
// extension section. Prefer RegisterFromConfig before New; this is for tests.
func RegisterFromEngine(eng *mow.Engine) error {
	if eng == nil {
		return fmt.Errorf("acp: nil engine")
	}
	var c Config
	if err := eng.Extension("acp", &c); err != nil {
		return err
	}
	agents := indexAgents(c.Agents)
	if len(agents) == 0 {
		return nil
	}
	ext.RegisterTool(&delegateTool{
		agents:    agents,
		workspace: eng.Workspace(),
		peerIdle:  peerIdleDuration(c.PeerIdleSec),
		peers:     map[string]*peerSlot{},
	})
	return nil
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

func indexAgents(list []AgentSpec) map[string]AgentSpec {
	m := map[string]AgentSpec{}
	for _, a := range list {
		name := strings.ToLower(strings.TrimSpace(a.Name))
		if name == "" || len(a.Command) == 0 {
			continue
		}
		if a.TimeoutSec <= 0 {
			a.TimeoutSec = 600
		}
		m[name] = a
	}
	return m
}

// peerSlot holds a long-lived ACP client + session for reuse across tool calls.
type peerSlot struct {
	client    *Client
	sessionID string
	dir       string
	lastUsed  time.Time
}

type delegateTool struct {
	agents    map[string]AgentSpec
	workspace string
	peerIdle  time.Duration // 0 = no idle eviction

	peersMu sync.Mutex
	peers   map[string]*peerSlot // key: agent\x00dir
}

func (t *delegateTool) Name() string { return "acp_delegate" }
func (t *delegateTool) Description() string {
	names := make([]string, 0, len(t.agents))
	for n := range t.agents {
		names = append(names, n)
	}
	return "Delegate a task to another harness via ACP (Agent Client Protocol). " +
		"Peer process/session is reused across calls when possible. " +
		"Args: agent (one of: " + strings.Join(names, ", ") + "), prompt (required), cwd (optional absolute or workspace-relative)."
}
func (t *delegateTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string"},"prompt":{"type":"string"},"cwd":{"type":"string"}},"required":["agent","prompt"]}`)
}

func peerKey(agent, dir string) string {
	return strings.ToLower(agent) + "\x00" + dir
}

func (t *delegateTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
		Cwd    string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	spec, ok := t.agents[strings.ToLower(strings.TrimSpace(a.Agent))]
	if !ok {
		return "", fmt.Errorf("acp_delegate: unknown agent %q", a.Agent)
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("acp_delegate: empty prompt")
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
	// Stay inside workspace when possible.
	if t.workspace != "" {
		absWS, _ := filepath.Abs(t.workspace)
		absDir, _ := filepath.Abs(dir)
		if absWS != "" && absDir != "" && !strings.HasPrefix(absDir, absWS) {
			return "", fmt.Errorf("acp_delegate: cwd %q escapes workspace", dir)
		}
	}

	to := time.Duration(spec.TimeoutSec) * time.Second
	pctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	slot, err := t.getOrStart(pctx, spec, dir)
	if err != nil {
		return "", err
	}

	agentName := spec.Name
	slot.client.OnChunk = func(delta string) {
		if eng := mow.EngineFromContext(ctx); eng != nil {
			eng.Emit(mow.Event{
				Type:  mow.EventDelegateChunk,
				Agent: agentName,
				Delta: delta,
			})
		}
	}
	defer func() { slot.client.OnChunk = nil }()

	reply, stop, err := slot.client.Prompt(pctx, slot.sessionID, prompt)
	slot.lastUsed = time.Now()
	if err != nil {
		// Drop dead peer so next call restarts.
		if !slot.client.Alive() || pctx.Err() != nil {
			t.dropPeer(spec.Name, dir)
		}
		return "", err
	}
	if strings.TrimSpace(reply) == "" {
		reply = "(peer returned no agent_message_chunk text; stopReason=" + stop + ")"
	}
	return fmt.Sprintf("agent: %s\nstop: %s\n\n%s", spec.Name, stop, reply), nil
}

func (t *delegateTool) getOrStart(ctx context.Context, spec AgentSpec, dir string) (*peerSlot, error) {
	key := peerKey(spec.Name, dir)
	t.peersMu.Lock()
	defer t.peersMu.Unlock()
	if t.peers == nil {
		t.peers = map[string]*peerSlot{}
	}
	t.evictIdleLocked(time.Now())

	if slot, ok := t.peers[key]; ok && slot != nil && slot.client != nil && slot.client.Alive() && slot.sessionID != "" {
		slot.lastUsed = time.Now()
		return slot, nil
	}
	// Dead or missing — (re)start.
	if slot, ok := t.peers[key]; ok && slot != nil && slot.client != nil {
		_ = slot.client.Close()
		delete(t.peers, key)
	}
	cl := &Client{Command: append([]string(nil), spec.Command...), Dir: dir}
	sid, err := cl.Start(ctx)
	if err != nil {
		return nil, err
	}
	slot := &peerSlot{client: cl, sessionID: sid, dir: dir, lastUsed: time.Now()}
	t.peers[key] = slot
	return slot, nil
}

// evictIdleLocked drops peers idle longer than peerIdle, or not Alive().
// Caller holds peersMu.
func (t *delegateTool) evictIdleLocked(now time.Time) {
	for k, slot := range t.peers {
		if slot == nil || slot.client == nil {
			delete(t.peers, k)
			continue
		}
		dead := !slot.client.Alive()
		idle := t.peerIdle > 0 && !slot.lastUsed.IsZero() && now.Sub(slot.lastUsed) > t.peerIdle
		if dead || idle {
			_ = slot.client.Close()
			delete(t.peers, k)
		}
	}
}

func (t *delegateTool) dropPeer(agent, dir string) {
	key := peerKey(agent, dir)
	t.peersMu.Lock()
	defer t.peersMu.Unlock()
	if slot, ok := t.peers[key]; ok && slot != nil {
		if slot.client != nil {
			_ = slot.client.Close()
		}
		delete(t.peers, key)
	}
}
