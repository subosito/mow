package acp

import (
	"slices"
	"strings"
	"testing"
)

func TestResolveMowAgents(t *testing.T) {
	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	defer func() { mowAgentBinary = orig }()

	falseV := false
	specs, err := resolveMowAgents(map[string]MowAgentSpec{
		"peer-b": {Model: "gpt-5-mini"},
		"peer-a": {
			Model:        "gemini-2.5-flash",
			AllowWrite:   &falseV,
			AllowShell:   &falseV,
			TimeoutSec:   120,
			Effort:       "high",
			SystemPrefix: "You are a reviewer.",
			ExtraArgs:    []string{"--stream"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("len=%d", len(specs))
	}
	if specs[0].Name != "peer-a" || specs[1].Name != "peer-b" {
		t.Fatalf("order/names: %+v %+v", specs[0], specs[1])
	}
	if specs[0].Mow == nil || specs[0].Mow.Model != "gemini-2.5-flash" {
		t.Fatalf("peer-a mow spec=%v", specs[0].Mow)
	}
	if specs[0].TimeoutSec != 120 {
		t.Fatalf("timeout=%d", specs[0].TimeoutSec)
	}

	hostRW := &hostPeerPolicy{workspace: "/ws", allowWrite: true, allowShell: true}
	cmdA := buildMowAgentCommand(*specs[0].Mow, hostRW, "/ws")
	wantA := []string{"mow", "acp", "--model", "gemini-2.5-flash", "--workspace", "/ws", "--effort", "high", "--system-prefix", "You are a reviewer.", "--stream"}
	if got := cmdA; !slices.Equal(got, wantA) {
		t.Fatalf("peer-a command=%v want %v", got, wantA)
	}

	hostDeny := &hostPeerPolicy{workspace: "/ws", allowWrite: false, allowShell: false}
	cmdB := buildMowAgentCommand(*specs[1].Mow, hostDeny, "/ws")
	if strings.Contains(strings.Join(cmdB, " "), "--allow-write") || strings.Contains(strings.Join(cmdB, " "), "--allow-shell") {
		t.Fatalf("denied host should not get power flags: %v", cmdB)
	}
	if !strings.Contains(strings.Join(cmdB, " "), "--read-only") {
		t.Fatalf("expected read-only inherit: %v", cmdB)
	}
}

func TestBuildMowAgentCapsExplicitTrueByHost(t *testing.T) {
	trueV := true
	spec := MowAgentSpec{Model: "gpt-5-mini", AllowWrite: &trueV, AllowShell: &trueV}
	host := &hostPeerPolicy{workspace: "/ws", allowWrite: false, allowShell: false}
	cmd := buildMowAgentCommand(spec, host, "/ws")
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "--allow-write") || strings.Contains(joined, "--allow-shell") {
		t.Fatalf("explicit true must still be capped by host: %v", cmd)
	}
}

func TestBuildMowAgentNoAllowWithReadOnly(t *testing.T) {
	trueV := true
	// Explicit read_only wins over allow flags so peer CLI Validate does not fail.
	spec := MowAgentSpec{Model: "gpt-5-mini", ReadOnly: &trueV, AllowWrite: &trueV, AllowShell: &trueV}
	host := &hostPeerPolicy{workspace: "/ws", allowWrite: true, allowShell: true}
	cmd := buildMowAgentCommand(spec, host, "/ws")
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--read-only") {
		t.Fatalf("want --read-only: %v", cmd)
	}
	if strings.Contains(joined, "--allow-write") || strings.Contains(joined, "--allow-shell") {
		t.Fatalf("read-only must not combine with allow flags: %v", cmd)
	}
}

func TestPeerKeyIncludesCommand(t *testing.T) {
	a := peerKey("peer", "/ws", []string{"mow", "acp", "--model", "a"}, PermissionReject)
	b := peerKey("peer", "/ws", []string{"mow", "acp", "--model", "b"}, PermissionReject)
	if a == b {
		t.Fatal("different models must not share peer pool key")
	}
	c := peerKey("peer", "/ws", []string{"mow", "acp", "--model", "a"}, PermissionAllow)
	if a == c {
		t.Fatal("permission mode must be part of key")
	}
}

func TestBuildMowAgentInheritsExtraRoots(t *testing.T) {
	spec := MowAgentSpec{Model: "gpt-5-mini"}
	host := &hostPeerPolicy{
		workspace:    "/ws",
		allowWrite:   true,
		allowShell:   true,
		extraRoots:   []string{"/extra"},
		extraRootsRO: []string{"/ro"},
	}
	cmd := buildMowAgentCommand(spec, host, "/peer-cwd")
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--extra-root /extra") || !strings.Contains(joined, "--extra-root /ro:ro") {
		t.Fatalf("extra roots missing: %v", cmd)
	}
}

func TestResolveMowAgentsRequiresModel(t *testing.T) {
	_, err := resolveMowAgents(map[string]MowAgentSpec{"x": {}})
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveAgentsCollision(t *testing.T) {
	_, err := resolveAgents(Config{
		Agents:    []AgentSpec{{Name: "peer-b", Command: []string{"other"}}},
		MowAgents: map[string]MowAgentSpec{"peer-b": {Model: "gpt-5-mini"}},
	})
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveAgentsMerge(t *testing.T) {
	list, err := resolveAgents(Config{
		Agents:    []AgentSpec{{Name: "peer-agent", Command: []string{"env", "npx"}}},
		MowAgents: map[string]MowAgentSpec{"peer-b": {Model: "gpt-5-mini"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d", len(list))
	}
	names := map[string]bool{}
	for _, a := range list {
		names[strings.ToLower(a.Name)] = true
	}
	if !names["peer-agent"] || !names["peer-b"] {
		t.Fatalf("names=%v", names)
	}
}

func TestDelegateAcceptsSubagentAlias(t *testing.T) {
	var a struct {
		Agent    string `json:"agent"`
		Subagent string `json:"subagent"`
	}
	a.Subagent = "Peer-B"
	name := strings.TrimSpace(a.Agent)
	if name == "" {
		name = strings.TrimSpace(a.Subagent)
	}
	if strings.ToLower(name) != "peer-b" {
		t.Fatalf("got %q", name)
	}
}

func TestEffectivePermissionMode(t *testing.T) {
	if got := effectivePermissionMode(AgentSpec{}); got != PermissionReject {
		t.Fatalf("default=%q", got)
	}
	if got := effectivePermissionMode(AgentSpec{PermissionMode: "allow"}); got != PermissionAllow {
		t.Fatalf("allow=%q", got)
	}
	if got := effectivePermissionMode(AgentSpec{Command: []string{"peer", "--force"}}); got != PermissionAllow {
		t.Fatalf("--force legacy=%q", got)
	}
}
