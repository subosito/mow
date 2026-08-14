package acp

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// MowAgentSpec is a native mow peer: expands to `mow acp --model …` so hosts
// can register multi-model agents without writing full ACP command lines.
//
//	extensions:
//	  acp:
//	    mow_agents:
//	      peer-agent:
//	        model: gpt-5-mini
//	        # allow_write: true   # nil = inherit host (capped by host)
//	        # allow_shell: true   # nil = inherit host (capped by host)
//	        # read_only: true     # nil = inherit (!host write && !host shell)
//	        # timeout_sec: 600    # default 600
//	        # effort: high        # optional --effort on the peer
//	        # dir: /abs/path      # optional peer cwd
//
// Same runtime as agents[] (ACP subprocess + acp_delegate). "Subagent" in other
// harnesses maps to these named agents.
type MowAgentSpec struct {
	// Model is required (gateway / provider model id for the peer Engine).
	Model string `yaml:"model" json:"model"`
	// AllowWrite enables --allow-write on the peer when true and the host allows
	// write. Nil inherits the host at delegate time (never exceeds host).
	AllowWrite *bool `yaml:"allow_write" json:"allow_write,omitempty"`
	// AllowShell enables --allow-shell on the peer when true and the host allows
	// shell. Nil inherits the host at delegate time (never exceeds host).
	AllowShell *bool `yaml:"allow_shell" json:"allow_shell,omitempty"`
	// ReadOnly sets --read-only on the peer. Nil inherits: true when the host
	// denies both write and shell.
	ReadOnly *bool `yaml:"read_only" json:"read_only,omitempty"`
	// TimeoutSec caps one delegated prompt (default 600 for mow agents).
	TimeoutSec int `yaml:"timeout_sec" json:"timeout_sec,omitempty"`
	// Effort sets peer reasoning intensity via --effort (mow CLI).
	Effort string `yaml:"effort" json:"effort,omitempty"`
	// SystemPrefix prepends optional identity or role text to the peer prompt.
	SystemPrefix string `yaml:"system_prefix" json:"system_prefix,omitempty"`
	// Dir is optional peer working directory (default: host workspace).
	Dir string `yaml:"dir" json:"dir,omitempty"`
	// ExtraArgs are appended after the standard mow acp flags (advanced).
	ExtraArgs []string `yaml:"extra_args" json:"extra_args,omitempty"`
}

// resolveMowAgents turns mow_agents map entries into AgentSpec rows. Command
// argv is filled at delegate time from host posture; only metadata is fixed here.
func resolveMowAgents(m map[string]MowAgentSpec) ([]AgentSpec, error) {
	if len(m) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]AgentSpec, 0, len(names))
	for _, name := range names {
		spec, err := resolveOneMowAgent(name, m[name])
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func resolveOneMowAgent(name string, s MowAgentSpec) (AgentSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AgentSpec{}, fmt.Errorf("acp: mow_agents entry with empty name")
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		return AgentSpec{}, fmt.Errorf("acp: mow_agents.%s: model is required", name)
	}
	timeout := s.TimeoutSec
	if timeout <= 0 {
		timeout = 600
	}
	copySpec := s
	return AgentSpec{
		Name:       name,
		Dir:        strings.TrimSpace(s.Dir),
		TimeoutSec: timeout,
		Mow:        &copySpec,
	}, nil
}

// buildMowAgentCommand constructs argv for a native peer at delegate time.
// Host posture caps permissions; credentials are never forwarded via argv.
func buildMowAgentCommand(spec MowAgentSpec, host *hostPeerPolicy, peerCwd string) []string {
	bin := mowAgentBinary()
	model := strings.TrimSpace(spec.Model)
	cmd := []string{bin, "acp", "--model", model}

	ws := strings.TrimSpace(peerCwd)
	if host != nil && strings.TrimSpace(host.workspace) != "" {
		ws = strings.TrimSpace(host.workspace)
	}
	if ws != "" {
		cmd = append(cmd, "--workspace", ws)
	}
	for _, r := range extraRootFlags(host) {
		cmd = append(cmd, "--extra-root", r)
	}

	// CLI Validate rejects --allow-write/--allow-shell with --read-only.
	// Prefer the more restrictive posture when both would apply.
	ro := effectiveReadOnly(&spec, host)
	aw := effectiveAllowWrite(&spec, host)
	as := effectiveAllowShell(&spec, host)
	if ro {
		cmd = append(cmd, "--read-only")
	} else {
		if aw {
			cmd = append(cmd, "--allow-write")
		}
		if as {
			cmd = append(cmd, "--allow-shell")
		}
	}
	if e := strings.TrimSpace(spec.Effort); e != "" {
		cmd = append(cmd, "--effort", e)
	}
	if prefix := strings.TrimSpace(spec.SystemPrefix); prefix != "" {
		cmd = append(cmd, "--system-prefix", prefix)
	}
	for _, a := range spec.ExtraArgs {
		a = strings.TrimSpace(a)
		if a != "" {
			cmd = append(cmd, a)
		}
	}
	return cmd
}

func extraRootFlags(host *hostPeerPolicy) []string {
	if host == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(path string, ro bool) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		flag := path
		if ro {
			flag = path + ":ro"
		}
		if seen[flag] {
			return
		}
		seen[flag] = true
		out = append(out, flag)
	}
	for _, r := range host.extraRoots {
		add(r, false)
	}
	for _, r := range host.extraRootsRO {
		add(r, true)
	}
	sort.Strings(out)
	return out
}

func effectiveReadOnly(spec *MowAgentSpec, host *hostPeerPolicy) bool {
	if spec != nil && spec.ReadOnly != nil {
		return *spec.ReadOnly
	}
	if host != nil {
		return !host.allowWrite && !host.allowShell
	}
	return true
}

// peerCommand builds argv for one delegate call (native or external).
func peerCommand(spec AgentSpec, host *hostPeerPolicy, peerCwd string) []string {
	if spec.Mow != nil {
		return buildMowAgentCommand(*spec.Mow, host, peerCwd)
	}
	cmd := append([]string(nil), spec.Command...)
	effort := strings.TrimSpace(spec.Effort)
	if effort == "" {
		return cmd
	}
	for _, a := range cmd {
		if a == "--reasoning-effort" || a == "--effort" {
			return cmd
		}
	}
	return append(cmd, "--reasoning-effort", effort)
}

// mowAgentBinary returns the executable path to use for the `acp` subcommand
// in mow_agents. It prefers os.Executable() so the host spawns itself
// (mow → mow acp) and falls back to "mow" when the executable path cannot be
// resolved (e.g. test harness or a custom binary).
var mowAgentBinary = defaultMowAgentBinary

func defaultMowAgentBinary() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		if fi, err := os.Stat(exe); err == nil && !fi.IsDir() {
			return exe
		}
	}
	return "mow"
}

// resolveAgents merges external agents[] with expanded mow_agents.
// Name collisions between the two lists are errors (fail closed).
func resolveAgents(c Config) ([]AgentSpec, error) {
	mowList, err := resolveMowAgents(c.MowAgents)
	if err != nil {
		return nil, err
	}
	if len(c.Agents) == 0 && len(mowList) == 0 {
		return nil, nil
	}
	seen := map[string]string{}
	for _, a := range c.Agents {
		n := strings.ToLower(strings.TrimSpace(a.Name))
		if n == "" {
			continue
		}
		seen[n] = "agents"
	}
	for _, a := range mowList {
		n := strings.ToLower(strings.TrimSpace(a.Name))
		if src, ok := seen[n]; ok {
			return nil, fmt.Errorf("acp: agent name %q appears in both %s and mow_agents", a.Name, src)
		}
		seen[n] = "mow_agents"
	}
	out := make([]AgentSpec, 0, len(c.Agents)+len(mowList))
	out = append(out, c.Agents...)
	out = append(out, mowList...)
	return out, nil
}
