package acp

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// resolveAgents validates extensions.acp.agents. Each row is either an
// external ACP command or a native mow peer (model), never both.
func resolveAgents(c Config) ([]AgentSpec, error) {
	if len(c.Agents) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]AgentSpec, 0, len(c.Agents))
	for i, a := range c.Agents {
		spec, err := resolveOneAgent(i, a)
		if err != nil {
			return nil, err
		}
		n := strings.ToLower(spec.Name)
		if seen[n] {
			return nil, fmt.Errorf("acp: duplicate agent name %q", spec.Name)
		}
		seen[n] = true
		out = append(out, spec)
	}
	return out, nil
}

func resolveOneAgent(i int, a AgentSpec) (AgentSpec, error) {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return AgentSpec{}, fmt.Errorf("acp: agents[%d]: name is required", i)
	}
	hasCmd := len(a.Command) > 0
	hasModel := strings.TrimSpace(a.Model) != ""
	switch {
	case hasCmd && hasModel:
		return AgentSpec{}, fmt.Errorf("acp: agent %q: set command or model, not both", name)
	case !hasCmd && !hasModel:
		return AgentSpec{}, fmt.Errorf("acp: agent %q: command (external) or model (native mow) is required", name)
	}
	timeout := a.TimeoutSec
	if timeout <= 0 {
		if hasModel {
			timeout = 600
		} else {
			timeout = 300
		}
	}
	a.Name = name
	a.Model = strings.TrimSpace(a.Model)
	a.Dir = strings.TrimSpace(a.Dir)
	a.TimeoutSec = timeout
	a.Effort = strings.TrimSpace(a.Effort)
	return a, nil
}

// buildMowAgentCommand constructs argv for a native peer at delegate time.
// Host posture caps permissions; credentials are never forwarded via argv.
func buildMowAgentCommand(spec AgentSpec, host *hostPeerPolicy, peerCwd string) []string {
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
	for _, arg := range spec.ExtraArgs {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			cmd = append(cmd, arg)
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

func effectiveReadOnly(spec *AgentSpec, host *hostPeerPolicy) bool {
	if spec != nil && spec.ReadOnly != nil {
		return *spec.ReadOnly
	}
	if host != nil {
		return !host.allowWrite && !host.allowShell
	}
	return true
}

// peerCommand builds argv for one delegate call (native or external).
// External argv is used as written — peer CLIs do not share one effort flag.
func peerCommand(spec AgentSpec, host *hostPeerPolicy, peerCwd string) []string {
	if spec.native() {
		return buildMowAgentCommand(spec, host, peerCwd)
	}
	return append([]string(nil), spec.Command...)
}

// mowAgentBinary returns the executable path to use for the `acp` subcommand
// on native agents. It prefers os.Executable() so the host spawns itself
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
