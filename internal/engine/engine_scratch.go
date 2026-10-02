package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/contextload"
	"github.com/subosito/mow/internal/session"
)

// sessionScratch creates a private temporary directory identified by home,
// workspace and session. Lstat rejects links and permissive preexisting roots.
func sessionScratch(workspace, id string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	if id == "" {
		id = session.NewID()
	}
	sum := sha256.Sum256([]byte(config.Home() + "\x00" + workspace + "\x00" + id))
	path := filepath.Join(os.TempDir(), "mow-scratch-"+hex.EncodeToString(sum[:16]))
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 || !scratchOwned(fi) {
		return "", fmt.Errorf("session scratch: refusing unsafe directory %s", path)
	}
	return path, nil
}

// setScratchLocked switches only the engine-owned root; old artifacts remain.
func (e *Engine) setScratchLocked(id string) error {
	path, err := sessionScratch(e.cfg.Workspace, id)
	if err != nil {
		return err
	}
	oldJail := contextload.PathJailFacts(e.cfg.Workspace, e.pol.ExtraRoots, e.pol.ExtraRootsReadOnly)
	if e.scratchDir != "" {
		e.pol.ExtraRoots = slices.DeleteFunc(e.pol.ExtraRoots, func(root string) bool { return root == e.scratchDir })
	}
	oldFacts := e.scratchFacts()
	e.scratchDir = path
	e.pol.ExtraRoots = append(e.pol.ExtraRoots, path)
	if oldFacts != "" {
		e.sys = strings.ReplaceAll(e.sys, oldFacts, "")
	}
	e.sys = strings.ReplaceAll(e.sys, oldJail, contextload.PathJailFacts(e.cfg.Workspace, e.pol.ExtraRoots, e.pol.ExtraRootsReadOnly))
	e.sys += "\n\n" + e.scratchFacts()
	return nil
}

func (e *Engine) scratchFacts() string {
	if e.scratchDir == "" {
		return ""
	}
	return fmt.Sprintf("Session scratch directory: %s\nUse this private temporary directory for cloned source, generated files and experiments instead of arbitrary /tmp paths. It is inside the file-tool jail; writes still require write permission. Contents are not loaded automatically and may be sent to the model if read. Closing the session does not delete it, but OS temporary-file cleanup may remove it; resume recreates an empty directory if missing. Store lasting findings in workspace internal docs when available.", e.scratchDir)
}
