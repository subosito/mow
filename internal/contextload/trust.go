package contextload

import (
	"github.com/subosito/mow/internal/config"
)

// ProjectTrusted reports whether the workspace has opted into project-local
// config/skills power. Trust is stored out-of-band under $MOW_HOME (`mow
// trust`) or granted per-invocation via MOW_TRUST_PROJECT=1 — never by a
// marker inside the workspace, which a cloned repo could ship.
func ProjectTrusted(workspace string) bool {
	return config.WorkspaceTrusted(workspace)
}
