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
	provider      string
}

func hostPolicyFromContext(ctx context.Context, fallbackWorkspace string) *hostPeerPolicy {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return nil
	}
	p := &hostPeerPolicy{
		workspace:     strings.TrimSpace(eng.Workspace()),
		extraRoots:    append([]string(nil), eng.ExtraRoots()...),
		extraRootsRO:  append([]string(nil), eng.ExtraRootsReadOnly()...),
		writableRoots: append([]string(nil), eng.WritableRoots()...),
		readOnly:      eng.ReadOnlyWorkspace(),
		allowWrite:    eng.AllowWrite(),
		allowShell:    eng.AllowShell(),
		provider:      strings.TrimSpace(eng.LLMProvider()),
	}
	if p.workspace == "" {
		p.workspace = strings.TrimSpace(fallbackWorkspace)
	}
	return p
}

func effectiveAllowWrite(spec *PeerSpec, host *hostPeerPolicy) bool {
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

func effectiveAllowShell(spec *PeerSpec, host *hostPeerPolicy) bool {
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
