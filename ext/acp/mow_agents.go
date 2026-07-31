package acp

import (
	"fmt"
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
//	        # allow_write: true   # default true
//	        # allow_shell: true   # default true
//	        # timeout_sec: 600    # default 600
//	        # effort: high        # optional --effort on the peer
//	        # dir: /abs/path      # optional peer cwd
//
// Same runtime as agents[] (ACP subprocess + acp_delegate). "Subagent" in other
// harnesses maps to these named agents.
type MowAgentSpec struct {
	// Model is required (gateway / provider model id for the peer Engine).
	Model string `yaml:"model" json:"model"`
	// AllowWrite enables --allow-write on the peer (default true when omitted).
	AllowWrite *bool `yaml:"allow_write" json:"allow_write,omitempty"`
	// AllowShell enables --allow-shell on the peer (default true when omitted).
	AllowShell *bool `yaml:"allow_shell" json:"allow_shell,omitempty"`
	// TimeoutSec caps one delegated prompt (default 600 for mow agents).
	TimeoutSec int `yaml:"timeout_sec" json:"timeout_sec,omitempty"`
	// Effort sets peer reasoning intensity via --effort (mow CLI).
	Effort string `yaml:"effort" json:"effort,omitempty"`
	// Dir is optional peer working directory (default: host workspace).
	Dir string `yaml:"dir" json:"dir,omitempty"`
	// ExtraArgs are appended after the standard mow acp flags (advanced).
	ExtraArgs []string `yaml:"extra_args" json:"extra_args,omitempty"`
}

// Config fields for native agents (see Config.MowAgents).

func boolDefaultTrue(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// expandMowAgents turns mow_agents map entries into AgentSpec rows.
func expandMowAgents(m map[string]MowAgentSpec) ([]AgentSpec, error) {
	if len(m) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names) // stable merge order
	out := make([]AgentSpec, 0, len(names))
	for _, name := range names {
		spec, err := expandOneMowAgent(name, m[name])
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func expandOneMowAgent(name string, s MowAgentSpec) (AgentSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AgentSpec{}, fmt.Errorf("acp: mow_agents entry with empty name")
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		return AgentSpec{}, fmt.Errorf("acp: mow_agents.%s: model is required", name)
	}
	cmd := []string{"mow", "acp", "--model", model}
	if boolDefaultTrue(s.AllowWrite) {
		cmd = append(cmd, "--allow-write")
	}
	if boolDefaultTrue(s.AllowShell) {
		cmd = append(cmd, "--allow-shell")
	}
	if e := strings.TrimSpace(s.Effort); e != "" {
		cmd = append(cmd, "--effort", e)
	}
	for _, a := range s.ExtraArgs {
		a = strings.TrimSpace(a)
		if a != "" {
			cmd = append(cmd, a)
		}
	}
	timeout := s.TimeoutSec
	if timeout <= 0 {
		timeout = 600 // coding peers default longer than generic 300
	}
	return AgentSpec{
		Name:       name,
		Command:    cmd,
		Dir:        strings.TrimSpace(s.Dir),
		TimeoutSec: timeout,
		// Effort on AgentSpec injects --reasoning-effort for non-mow peers;
		// mow peers already got --effort above.
	}, nil
}

// resolveAgents merges external agents[] with expanded mow_agents.
// Name collisions between the two lists are errors (fail closed).
func resolveAgents(c Config) ([]AgentSpec, error) {
	mowList, err := expandMowAgents(c.MowAgents)
	if err != nil {
		return nil, err
	}
	if len(c.Agents) == 0 && len(mowList) == 0 {
		return nil, nil
	}
	// Collision check (case-insensitive names, same as indexAgents).
	seen := map[string]string{} // lower name → source
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
