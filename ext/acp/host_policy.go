package acp

import (
	"context"
	"strings"

	"github.com/subosito/mow"
)

// hostPeerPolicy captures the running host Engine posture for native mow peers.
// Nil means no host in context — conservative defaults apply at delegate time.
type hostPeerPolicy struct {
	workspace     string
	extraRoots    []string
	extraRootsRO  []string
	writableRoots []string
	readOnly      bool
	allowWrite    bool
	allowShell    bool
}

// peerHost is the mow.Engine surface used when building native peer argv.
type peerHost interface {
	AllowWrite() bool
	AllowShell() bool
	Workspace() string
	ExtraRoots() []string
	ExtraRootsReadOnly() []string
	WritableRoots() []string
	ReadOnlyWorkspace() bool
}

func hostPolicyFromContext(ctx context.Context, fallbackWorkspace string) *hostPeerPolicy {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return nil
	}
	h := peerHost(eng)
	p := &hostPeerPolicy{
		workspace:     strings.TrimSpace(h.Workspace()),
		extraRoots:    append([]string(nil), h.ExtraRoots()...),
		extraRootsRO:  append([]string(nil), h.ExtraRootsReadOnly()...),
		writableRoots: append([]string(nil), h.WritableRoots()...),
		readOnly:      h.ReadOnlyWorkspace(),
		allowWrite:    h.AllowWrite(),
		allowShell:    h.AllowShell(),
	}
	if p.workspace == "" {
		p.workspace = strings.TrimSpace(fallbackWorkspace)
	}
	return p
}

func effectiveAllowWrite(spec *MowAgentSpec, host *hostPeerPolicy) bool {
	want := false
	if spec != nil && spec.AllowWrite != nil {
		want = *spec.AllowWrite
	} else if host != nil {
		want = host.allowWrite
	}
	if host != nil && !host.allowWrite {
		want = false
	}
	return want
}

func effectiveAllowShell(spec *MowAgentSpec, host *hostPeerPolicy) bool {
	want := false
	if spec != nil && spec.AllowShell != nil {
		want = *spec.AllowShell
	} else if host != nil {
		want = host.allowShell
	}
	if host != nil && !host.allowShell {
		want = false
	}
	return want
}
