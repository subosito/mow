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
	Config       string
	Workspace    string
	ExtraRoots   []string // repeatable --extra-root
	Model        string
	Effort       string
	BaseURL      string
	SystemPrefix []string // repeatable --system-prefix
	AllowShell   bool
	AllowWrite   bool
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
	fs.Var((*stringList)(&f.ExtraRoots), "extra-root", "extra FS root for path jail (repeatable)")
	fs.StringVar(&f.Model, "model", "", "model id")
	fs.StringVar(&f.Effort, "effort", "", "reasoning effort (catalog efforts when listed; else none|low|medium|high)")
	fs.StringVar(&f.BaseURL, "base-url", "", "LLM base URL")
	fs.Var((*stringList)(&f.SystemPrefix), "system-prefix", "system prompt prefix (repeatable)")
	fs.BoolVar(&f.AllowShell, "allow-shell", false, "enable bash")
	fs.BoolVar(&f.AllowWrite, "allow-write", false, "enable write/edit")
	fs.Var(&maxTurnsFlag{f: f}, "max-turns", "max agent turns per Prompt (0=unlimited)")
	fs.BoolVar(&f.NoSession, "no-session", false, "do not persist session")
	fs.StringVar(&f.SessionID, "session", "", "session id")
	fs.BoolVar(&f.Continue, "continue", false, "resume latest session")
	fs.BoolVar(&f.Stream, "stream", false, "stream token deltas")
	fs.BoolVar(&f.Verbose, "verbose", false, "debug lifecycle logs (run/tool) on stderr")
}

// stringList is a repeatable flag.String-like value (append on each Set).
type stringList []string

func (s *stringList) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	*s = append(*s, v)
	return nil
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
		ConfigPaths:   paths,
		Workspace:     f.Workspace,
		ExtraRoots:    append([]string(nil), f.ExtraRoots...),
		Model:         f.Model,
		ExplicitModel: strings.TrimSpace(f.Model) != "",
		Effort:        f.Effort,
		BaseURL:       f.BaseURL,
		SystemPrefix:  append([]string(nil), f.SystemPrefix...),
		AllowWrite:    f.AllowWrite,
		AllowShell:    f.AllowShell,
		NoSession:     f.NoSession,
		SessionID:     f.SessionID,
		Continue:      f.Continue,
		Stream:        f.Stream,
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
