package config

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvHome is the environment variable that overrides the default user data dir.
const EnvHome = "MOW_HOME"

// Home returns the mow user data directory (config.yaml, sessions, skills, AGENTS.md).
//
//	MOW_HOME set  → that path (tilde-expanded, cleaned to absolute when possible)
//	unset         → ~/.mow  (or ".mow" if $HOME is unavailable)
//
// Project-local paths under workspace/.mow are independent of Home.
func Home() string {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		if strings.HasPrefix(v, "~"+string(os.PathSeparator)) || v == "~" {
			if h, err := os.UserHomeDir(); err == nil {
				if v == "~" {
					v = h
				} else {
					v = filepath.Join(h, v[2:])
				}
			}
		}
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
		return filepath.Clean(v)
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".mow")
	}
	return ".mow"
}

// ConfigPath is Home()/config.yaml — the default user config file.
func ConfigPath() string {
	return filepath.Join(Home(), "config.yaml")
}

// SessionsDir is Home()/sessions — default session.dir when unset in yaml.
func SessionsDir() string {
	return filepath.Join(Home(), "sessions")
}

// SkillsDir is Home()/skills — global skill markdown directory.
func SkillsDir() string {
	return filepath.Join(Home(), "skills")
}

// AgentsPath is Home()/AGENTS.md — optional global instructions file.
func AgentsPath() string {
	return filepath.Join(Home(), "AGENTS.md")
}
