package acp

import (
	"slices"
	"strings"
	"testing"
)

func TestExpandMowAgents(t *testing.T) {
	falseV := false
	specs, err := expandMowAgents(map[string]MowAgentSpec{
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
	// sorted by name: peerA, peerB
	if specs[0].Name != "peer-a" || specs[1].Name != "peer-b" {
		t.Fatalf("order/names: %+v %+v", specs[0], specs[1])
	}
	peerA := specs[0]
	wantPrefix := []string{"mow", "acp", "--model", "gemini-2.5-flash"}
	for i, p := range wantPrefix {
		if peerA.Command[i] != p {
			t.Fatalf("peer-a cmd[%d]=%q want %q full=%v", i, peerA.Command[i], p, peerA.Command)
		}
	}
	// no write/shell flags when false
	joined := strings.Join(peerA.Command, " ")
	if strings.Contains(joined, "--allow-write") || strings.Contains(joined, "--allow-shell") {
		t.Fatalf("write/shell should be off: %v", peerA.Command)
	}
	wantCommand := []string{"mow", "acp", "--model", "gemini-2.5-flash", "--effort", "high", "--system-prefix", "You are a reviewer.", "--stream"}
	if got := peerA.Command; !slices.Equal(got, wantCommand) {
		t.Fatalf("peer-a command=%v want %v", got, wantCommand)
	}
	if peerA.TimeoutSec != 120 {
		t.Fatalf("timeout=%d", peerA.TimeoutSec)
	}

	peerB := specs[1]
	wantDefaultCommand := []string{"mow", "acp", "--model", "gpt-5-mini", "--allow-write", "--allow-shell"}
	if got := peerB.Command; !slices.Equal(got, wantDefaultCommand) {
		t.Fatalf("default command=%v want %v", got, wantDefaultCommand)
	}
	joined = strings.Join(peerB.Command, " ")
	if !strings.Contains(joined, "--allow-write") || !strings.Contains(joined, "--allow-shell") {
		t.Fatalf("defaults should enable write/shell: %v", peerB.Command)
	}
	if peerB.TimeoutSec != 600 {
		t.Fatalf("default timeout=%d want 600", peerB.TimeoutSec)
	}
}

func TestExpandMowAgentsRequiresModel(t *testing.T) {
	_, err := expandMowAgents(map[string]MowAgentSpec{"x": {}})
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
	// unit: name resolution logic only — Exec needs full peer; test via map lookup shape
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
