package contextload

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectTrusted reports whether the workspace has opted into project-local
// config/skills power (file .mow/trust or MOW_TRUST_PROJECT=1/true).
func ProjectTrusted(workspace string) bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("MOW_TRUST_PROJECT"))); v == "1" || v == "true" || v == "yes" {
		return true
	}
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(ws, ".mow", "trust"))
	return err == nil
}
