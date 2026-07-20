// Package cliutil builds a mow.Engine from common CLI flags.
// Not a pack: no tools, commands, or hooks are registered here.
package cliutil

import (
	"flag"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

// EngineFlags are common flags for any command that constructs a mow.Engine.
type EngineFlags struct {
	Config     string
	Workspace  string
	Model      string
	BaseURL    string
	AllowShell bool
	AllowWrite bool
	MaxTurns   int
	NoSession  bool
	SessionID  string
	Continue   bool
	Stream     bool
}

// Bind registers flags on fs.
func (f *EngineFlags) Bind(fs *flag.FlagSet) {
	fs.StringVar(&f.Config, "config", "", "optional config yaml")
	fs.StringVar(&f.Workspace, "workspace", "", "workspace root")
	fs.StringVar(&f.Model, "model", "", "model id")
	fs.StringVar(&f.BaseURL, "base-url", "", "LLM base URL")
	fs.BoolVar(&f.AllowShell, "allow-shell", false, "enable bash")
	fs.BoolVar(&f.AllowWrite, "allow-write", false, "enable write/edit")
	fs.IntVar(&f.MaxTurns, "max-turns", 0, "max agent turns")
	fs.BoolVar(&f.NoSession, "no-session", false, "do not persist session")
	fs.StringVar(&f.SessionID, "session", "", "session id")
	fs.BoolVar(&f.Continue, "continue", false, "resume latest session")
	fs.BoolVar(&f.Stream, "stream", false, "stream token deltas")
}

// ConfigPaths returns paths for mow.New.
func (f *EngineFlags) ConfigPaths() []string {
	if strings.TrimSpace(f.Config) == "" {
		return nil
	}
	return []string{f.Config}
}

// Options builds mow.Options from flags (explicit overrides, no process env mutation).
// Calls ext.BeforeNew so packs that register config-driven tools can run before mow.New.
func (f *EngineFlags) Options() mow.Options {
	paths := f.ConfigPaths()
	_ = ext.BeforeNew(paths...)
	return mow.Options{
		ConfigPaths: paths,
		Workspace:   f.Workspace,
		Model:       f.Model,
		BaseURL:     f.BaseURL,
		AllowWrite:  f.AllowWrite,
		AllowShell:  f.AllowShell,
		NoSession:   f.NoSession,
		SessionID:   f.SessionID,
		Continue:    f.Continue,
		MaxTurns:    f.MaxTurns,
		Stream:      f.Stream,
	}
}

// NewEngine runs BeforeNew hooks and constructs an Engine.
func (f *EngineFlags) NewEngine() (*mow.Engine, error) {
	return mow.New(f.Options())
}
