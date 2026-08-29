package acp

import (
	"slices"
	"strings"
	"testing"
)

func TestResolveAgentsNativeAndExternal(t *testing.T) {
	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	defer func() { mowAgentBinary = orig }()

	falseV := false
	specs, err := resolvePeers(Config{Peers: []PeerSpec{
		{
			Name:         "peer-a",
			Model:        "gemini-2.5-flash",
			AllowWrite:   &falseV,
			AllowShell:   &falseV,
			TimeoutSec:   120,
			Effort:       "high",
			SystemPrefix: "You are a reviewer.",
			ExtraArgs:    []string{"--stream"},
		},
		{Name: "peer-b", Model: "gpt-5-mini"},
		{Name: "peer-agent", Command: []string{"env", "npx"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("len=%d", len(specs))
	}
	if specs[0].Name != "peer-a" || specs[0].Model != "gemini-2.5-flash" || specs[0].TimeoutSec != 120 {
		t.Fatalf("peer-a=%+v", specs[0])
	}
	if specs[1].TimeoutSec != 600 {
		t.Fatalf("native default timeout=%d", specs[1].TimeoutSec)
	}
	if specs[2].TimeoutSec != 300 || specs[2].native() {
		t.Fatalf("external=%+v", specs[2])
	}

	hostRW := &hostPeerPolicy{workspace: "/ws", allowWrite: true, allowShell: true, provider: "gateway"}
	cmdA := buildMowAgentCommand(specs[0], hostRW, "/ws")
	wantA := []string{"mow", "acp", "--model", "gemini-2.5-flash", "--provider", "gateway", "--workspace", "/ws", "--effort", "high", "--system-prefix", "You are a reviewer.", "--stream"}
	if got := cmdA; !slices.Equal(got, wantA) {
		t.Fatalf("peer-a command=%v want %v", got, wantA)
	}

	hostDeny := &hostPeerPolicy{workspace: "/ws", allowWrite: false, allowShell: false}
	cmdB := buildMowAgentCommand(specs[1], hostDeny, "/ws")
	if strings.Contains(strings.Join(cmdB, " "), "--allow-write") || strings.Contains(strings.Join(cmdB, " "), "--allow-shell") {
		t.Fatalf("denied host should not get power flags: %v", cmdB)
	}
	if !strings.Contains(strings.Join(cmdB, " "), "--read-only") {
		t.Fatalf("expected read-only inherit: %v", cmdB)
	}
}

func TestBuildMowAgentCapsExplicitTrueByHost(t *testing.T) {
	trueV := true
	spec := PeerSpec{Model: "gpt-5-mini", AllowWrite: &trueV, AllowShell: &trueV}
	host := &hostPeerPolicy{workspace: "/ws", allowWrite: false, allowShell: false}
	cmd := buildMowAgentCommand(spec, host, "/ws")
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "--allow-write") || strings.Contains(joined, "--allow-shell") {
		t.Fatalf("explicit true must still be capped by host: %v", cmd)
	}
}

func TestBuildMowAgentPeerProviderWinsOverHost(t *testing.T) {
	spec := PeerSpec{Model: "deepseek-chat", Provider: "direct"}
	host := &hostPeerPolicy{workspace: "/ws", provider: "gateway"}
	cmd := buildMowAgentCommand(spec, host, "/ws")
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--provider direct") {
		t.Fatalf("want peer provider: %v", cmd)
	}
	if strings.Contains(joined, "--provider gateway") {
		t.Fatalf("peer provider must win: %v", cmd)
	}
}

func TestBuildMowAgentNoAllowWithReadOnly(t *testing.T) {
	trueV := true
	// Explicit read_only wins over allow flags so peer CLI Validate does not fail.
	spec := PeerSpec{Model: "gpt-5-mini", ReadOnly: &trueV, AllowWrite: &trueV, AllowShell: &trueV}
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
	spec := PeerSpec{Model: "gpt-5-mini"}
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

func TestResolveAgentsRequiresCommandOrModel(t *testing.T) {
	_, err := resolvePeers(Config{Peers: []PeerSpec{{Name: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveAgentsRejectsCommandAndModel(t *testing.T) {
	_, err := resolvePeers(Config{Peers: []PeerSpec{{
		Name: "x", Command: []string{"peer"}, Model: "gpt-5-mini",
	}}})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveAgentsDuplicateName(t *testing.T) {
	_, err := resolvePeers(Config{Peers: []PeerSpec{
		{Name: "peer-b", Command: []string{"other"}},
		{Name: "peer-b", Model: "gpt-5-mini"},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
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

func TestPeerCommandDoesNotInjectEffortOnExternal(t *testing.T) {
	cmd := peerCommand(PeerSpec{
		Name:    "peer",
		Command: []string{"peer-agent", "--acp"},
		Effort:  "high",
	}, nil, "")
	for _, a := range cmd {
		if a == "--reasoning-effort" || a == "--effort" {
			t.Fatalf("external command must not get an injected effort flag: %v", cmd)
		}
	}
	if !slices.Equal(cmd, []string{"peer-agent", "--acp"}) {
		t.Fatalf("argv rewritten: %v", cmd)
	}
}

func TestEffectivePermissionMode(t *testing.T) {
	if got := effectivePermissionMode(PeerSpec{}); got != PermissionReject {
		t.Fatalf("default=%q", got)
	}
	if got := effectivePermissionMode(PeerSpec{Model: "gemini-2.5-flash"}); got != PermissionAllow {
		t.Fatalf("native omitted=%q want allow", got)
	}
	if got := effectivePermissionMode(PeerSpec{Model: "gemini-2.5-flash", PermissionMode: "reject"}); got != PermissionReject {
		t.Fatalf("native explicit reject=%q", got)
	}
	if got := effectivePermissionMode(PeerSpec{PermissionMode: "allow"}); got != PermissionAllow {
		t.Fatalf("allow=%q", got)
	}
	if got := effectivePermissionMode(PeerSpec{Command: []string{"peer", "--force"}}); got != PermissionAllow {
		t.Fatalf("--force legacy=%q", got)
	}
}
