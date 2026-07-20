package mow

import (
	"github.com/subosito/mow/internal/config"
)

// Workspace trust gates project-local power (workspace/.mow/config.yaml and
// skills). The trust list lives out-of-band under $MOW_HOME/trusted — never
// inside the workspace, where a cloned repo could grant itself trust.
// MOW_TRUST_PROJECT=1 overrides per invocation (CI, tests).

// WorkspaceTrusted reports whether workspace may load project-local config
// and skills.
func WorkspaceTrusted(workspace string) bool {
	return config.WorkspaceTrusted(workspace)
}

// TrustWorkspace adds workspace to the trust list (idempotent).
func TrustWorkspace(workspace string) error {
	return config.TrustWorkspace(workspace)
}

// RevokeWorkspaceTrust removes workspace from the trust list (idempotent).
func RevokeWorkspaceTrust(workspace string) error {
	return config.RevokeWorkspace(workspace)
}

// TrustedWorkspaces returns the trusted workspace paths.
func TrustedWorkspaces() []string {
	return config.TrustedWorkspaces()
}
