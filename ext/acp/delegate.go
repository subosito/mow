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
	// mu serializes use of the peer (a peer is a single stdio conversation):
	// held for the whole delegate call, covering OnChunk set/clear and Prompt.
	mu        sync.Mutex
	client    *Client
	sessionID string
	dir       string
	// lastUsed is guarded by delegateTool.peersMu.
	lastUsed time.Time
	// starting is non-nil while the peer is being spawned (reserved slot);
	// closed when the spawn finishes. Guarded by delegateTool.peersMu.
	starting chan struct{}
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
	// Stay inside workspace when possible (symlink-resolving jail; a plain
	// prefix check would let sibling dirs like /ws-evil pass for /ws).
	if t.workspace != "" {
		resolved, err := resolveInWorkspace(t.workspace, dir)
		if err != nil {
			return "", fmt.Errorf("acp_delegate: cwd %q escapes workspace", dir)
		}
		dir = resolved
	}

	to := time.Duration(spec.TimeoutSec) * time.Second
	pctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	slot, err := t.getOrStart(pctx, spec, dir)
	if err != nil {
		return "", err
	}

	// One delegate call at a time per peer: a peer is a single stdio
	// conversation, so OnChunk and the reply accumulator must not be shared.
	slot.mu.Lock()
	defer slot.mu.Unlock()

	agentName := spec.Name
	slot.client.SetOnChunk(func(delta string) {
		if eng := mow.EngineFromContext(ctx); eng != nil {
			eng.Emit(mow.Event{
				Type:  mow.EventDelegateChunk,
				Agent: agentName,
				Delta: delta,
			})
		}
	})
	defer slot.client.SetOnChunk(nil)

	reply, stop, err := slot.client.Prompt(pctx, slot.sessionID, prompt)
	t.peersMu.Lock()
	slot.lastUsed = time.Now()
	t.peersMu.Unlock()
	if err != nil {
		// Drop dead peer so next call restarts.
		if !slot.client.Alive() || pctx.Err() != nil {
			t.dropPeer(peerKey(spec.Name, dir), slot)
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
		res := &peerSlot{dir: dir, starting: make(chan struct{})}
		t.peers[key] = res
		t.peersMu.Unlock()

		cl := &Client{Command: append([]string(nil), spec.Command...), Dir: dir}
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
