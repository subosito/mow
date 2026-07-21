// Package cliutil builds a mow.Engine from common CLI flags.
// Not a pack: no tools, commands, or hooks are registered here.
package cliutil

import (
	"flag"
	"strconv"
	"strings"

	"github.com/subosito/mow"
)

// EngineFlags are common flags for any command that constructs a mow.Engine.
type EngineFlags struct {
	Config     string
	Workspace  string
	Model      string
	BaseURL    string
	AllowShell bool
	AllowWrite bool
	// MaxTurns is the parsed --max-turns value. Only applied when MaxTurnsSet
	// (omit flag → config default; --max-turns 0 → unlimited).
	MaxTurns    int
	MaxTurnsSet bool
	NoSession   bool
	SessionID   string
	Continue    bool
	Stream      bool
	Verbose     bool
}

// Bind registers flags on fs.
func (f *EngineFlags) Bind(fs *flag.FlagSet) {
	fs.StringVar(&f.Config, "config", "", "optional config yaml")
	fs.StringVar(&f.Workspace, "workspace", "", "workspace root")
	fs.StringVar(&f.Model, "model", "", "model id")
	fs.StringVar(&f.BaseURL, "base-url", "", "LLM base URL")
	fs.BoolVar(&f.AllowShell, "allow-shell", false, "enable bash")
	fs.BoolVar(&f.AllowWrite, "allow-write", false, "enable write/edit")
	fs.Var(&maxTurnsFlag{f: f}, "max-turns", "max agent turns per Prompt (0=unlimited)")
	fs.BoolVar(&f.NoSession, "no-session", false, "do not persist session")
	fs.StringVar(&f.SessionID, "session", "", "session id")
	fs.BoolVar(&f.Continue, "continue", false, "resume latest session")
	fs.BoolVar(&f.Stream, "stream", false, "stream token deltas")
	fs.BoolVar(&f.Verbose, "verbose", false, "debug lifecycle logs (run/tool) on stderr")
}

// maxTurnsFlag tracks whether --max-turns was explicitly set so 0 can mean
// unlimited without collapsing onto "use config" (the zero value when omitted).
type maxTurnsFlag struct{ f *EngineFlags }

func (m *maxTurnsFlag) String() string {
	if m == nil || m.f == nil {
		return "0"
	}
	return strconv.Itoa(m.f.MaxTurns)
}

func (m *maxTurnsFlag) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	m.f.MaxTurns = n
	m.f.MaxTurnsSet = true
	return nil
}

// ConfigPaths returns paths for mow.New.
func (f *EngineFlags) ConfigPaths() []string {
	p := strings.TrimSpace(f.Config)
	if p == "" {
		return nil
	}
	return []string{p}
}

// Options builds mow.Options from flags (explicit overrides, no process env mutation).
// mow.New runs ext.BeforeNew itself (and surfaces its errors), so no pack
// setup happens here.
func (f *EngineFlags) Options() mow.Options {
	paths := f.ConfigPaths()
	opt := mow.Options{
		ConfigPaths: paths,
		Workspace:   f.Workspace,
		Model:       f.Model,
		BaseURL:     f.BaseURL,
		AllowWrite:  f.AllowWrite,
		AllowShell:  f.AllowShell,
		NoSession:   f.NoSession,
		SessionID:   f.SessionID,
		Continue:    f.Continue,
		Stream:      f.Stream,
	}
	if f.MaxTurnsSet {
		if f.MaxTurns == 0 {
			// Options uses negative as the unlimited override (0 leaves config).
			opt.MaxTurns = -1
		} else {
			opt.MaxTurns = f.MaxTurns
		}
	}
	return opt
}

// NewEngine runs BeforeNew hooks and constructs an Engine.
func (f *EngineFlags) NewEngine() (*mow.Engine, error) {
	return mow.New(f.Options())
}
