package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace trust lives out-of-band under Home()/trusted — one absolute
// workspace path per line. It must never live inside the workspace itself:
// a cloned repo could ship the marker and grant itself trust with no user
// action (the direnv problem). MOW_TRUST_PROJECT=1 remains a per-invocation
// override for CI and tests.

// TrustedPath is Home()/trusted — the workspace trust list.
func TrustedPath() string {
	return filepath.Join(Home(), "trusted")
}

// WorkspaceTrusted reports whether workspace may load project-local config
// and skills (workspace/.mow/*).
func WorkspaceTrusted(workspace string) bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("MOW_TRUST_PROJECT"))); v == "1" || v == "true" || v == "yes" {
		return true
	}
	ws, ok := canonicalWorkspace(workspace)
	if !ok {
		return false
	}
	for _, t := range TrustedWorkspaces() {
		if t == ws {
			return true
		}
	}
	return false
}

// TrustedWorkspaces returns the trust list (canonicalized, comments skipped).
// The file must be a regular file with no group/other permissions (0600);
// anything else is treated as untrusted so a tampered or world-writable list
// can never grant trust.
func TrustedWorkspaces() []string {
	path := TrustedPath()
	fi, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if ws, ok := canonicalWorkspace(line); ok {
			out = append(out, ws)
		}
	}
	return out
}

// TrustWorkspace adds workspace to the trust list (idempotent).
func TrustWorkspace(workspace string) error {
	ws, ok := canonicalWorkspace(workspace)
	if !ok {
		return fmt.Errorf("trust: cannot resolve workspace %q", workspace)
	}
	cur := TrustedWorkspaces()
	for _, t := range cur {
		if t == ws {
			return nil
		}
	}
	return writeTrusted(append(cur, ws))
}

// RevokeWorkspace removes workspace from the trust list (idempotent).
func RevokeWorkspace(workspace string) error {
	ws, ok := canonicalWorkspace(workspace)
	if !ok {
		return fmt.Errorf("trust: cannot resolve workspace %q", workspace)
	}
	cur := TrustedWorkspaces()
	var keep []string
	for _, t := range cur {
		if t != ws {
			keep = append(keep, t)
		}
	}
	if len(keep) == len(cur) {
		return nil
	}
	if len(keep) == 0 {
		err := os.Remove(TrustedPath())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeTrusted(keep)
}

// writeTrusted replaces the trust list atomically (temp file + rename) so
// concurrent trust/revoke invocations cannot interleave partial writes, and
// the result always has 0600 permissions.
func writeTrusted(workspaces []string) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	dir := Home()
	tmp, err := os.CreateTemp(dir, ".trusted-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(workspaces, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, TrustedPath())
}

// canonicalWorkspace makes a path absolute, clean, and symlink-resolved so a
// workspace trusted under one spelling matches every later spelling.
func canonicalWorkspace(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return abs, true
}
