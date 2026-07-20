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
func TrustedWorkspaces() []string {
	raw, err := os.ReadFile(TrustedPath())
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
	for _, t := range TrustedWorkspaces() {
		if t == ws {
			return nil
		}
	}
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(TrustedPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(ws + "\n")
	return err
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
	return os.WriteFile(TrustedPath(), []byte(strings.Join(keep, "\n")+"\n"), 0o600)
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
