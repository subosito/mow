package policy

import (
	"os/exec"

	"github.com/subosito/mow/internal/sandbox"
)

// SandboxBackend returns the shell sandbox backend for this policy, built once
// and reused. Sandbox mode is only meaningful when a shell exists, so it is
// ignored (identity backend) when AllowShell is false.
//
// An unavailable backend (bwrap missing, wrong OS) is an error, not a silent
// fallback: an operator who asked for a jail must never get a raw shell.
func (p *Policy) SandboxBackend() (sandbox.Backend, error) {
	if p == nil {
		return sandbox.None{}, nil
	}
	p.sandboxOnce.Do(func() {
		if !p.AllowShell || p.Sandbox == "" || p.Sandbox == sandbox.ModeNone {
			p.sandboxBE = sandbox.None{}
			return
		}
		spec := sandbox.Spec{Workspace: p.Workspace}
		roots, err := p.jailRoots()
		if err == nil {
			for _, r := range roots {
				spec.Roots = append(spec.Roots, sandbox.Root{Path: r.Path, ReadOnly: r.ReadOnly})
			}
		}
		p.sandboxBE, p.sandboxErr = sandbox.New(p.Sandbox, spec)
	})
	return p.sandboxBE, p.sandboxErr
}

// SandboxEnabled reports whether shell commands actually run inside a jail
// (used by tool descriptions and status output).
func (p *Policy) SandboxEnabled() bool {
	return p != nil && p.AllowShell && p.Sandbox != "" && p.Sandbox != sandbox.ModeNone
}

// WrapShell applies the sandbox backend to a shell command. Callers keep
// ownership of process-group / session setup; Wrap preserves SysProcAttr.
func (p *Policy) WrapShell(cmd *exec.Cmd) (*exec.Cmd, error) {
	be, err := p.SandboxBackend()
	if err != nil {
		return nil, err
	}
	if be == nil {
		return cmd, nil
	}
	return be.Wrap(cmd)
}
