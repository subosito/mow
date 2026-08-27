// Package cliutil builds a mow.Engine from common CLI flags.
// Not a pack: no tools, commands, or hooks are registered here.
package cliutil

import (
	"flag"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/internal/sandbox"
)

// EngineFlags are common flags for any command that constructs a mow.Engine.
type EngineFlags struct {
	Config        string
	Workspace     string
	ExtraRoots    []string // repeatable --extra-root
	Model         string
	Effort        string
	BaseURL       string
	SystemPrefix  []string // repeatable --system-prefix
	AllowShell    bool
	AllowWrite    bool
	DisallowShell bool
	DisallowWrite bool
	ReadOnly      bool
	// Sandbox is --sandbox: bwrap when the flag is present (bare form), else
	// empty (= none). Linux only — the flag is not registered off-Linux since
	// no sandbox backend exists there. An invalid value always errors.
	Sandbox string
	// MaxTurns is the parsed --max-turns value. Only applied when MaxTurnsSet
	// (omit flag → config default; --max-turns 0 → unlimited).
	MaxTurns    int
	MaxTurnsSet bool
	NoSession   bool
	SessionID   string
	Continue    bool
	Stream      bool
	Verbose     bool
	Skills      []string // repeatable --skill
}

// Bind registers flags on fs.
func (f *EngineFlags) Bind(fs *flag.FlagSet) {
	fs.StringVar(&f.Config, "config", "", "optional config yaml")
	fs.StringVar(&f.Workspace, "workspace", "", "workspace root: a profile name from $MOW_HOME/workspaces/<name> or a directory path")
	fs.Var((*stringList)(&f.ExtraRoots), "extra-root", "extra FS root for path jail (repeatable; PATH, PATH:ro, or explicit PATH:rw)")
	fs.StringVar(&f.Model, "model", "", "model id")
	fs.StringVar(&f.Effort, "effort", "", "reasoning effort (catalog efforts when listed; else none|low|medium|high)")
	fs.StringVar(&f.BaseURL, "base-url", "", "LLM base URL")
	fs.Var((*stringList)(&f.SystemPrefix), "system-prefix", "system prompt prefix (repeatable)")
	fs.BoolVar(&f.AllowShell, "allow-shell", false, "enable bash/proc (not path-jailed; see --sandbox)")
	// Linux only: off-Linux there is no sandbox backend, so the flag is not
	// registered at all (hidden), and --sandbox is an "unknown flag" error.
	if runtime.GOOS == "linux" {
		fs.Var(&sandboxFlagValue{f: f}, "sandbox", "wrap bash/proc in a bubblewrap jail (host fs read-only, workspace rw, network on; --sandbox=none disables)")
	}
	fs.BoolVar(&f.AllowWrite, "allow-write", false, "enable write/edit")
	fs.BoolVar(&f.DisallowShell, "disallow-shell", false, "disable bash even when enabled in config")
	fs.BoolVar(&f.DisallowWrite, "disallow-write", false, "disable write/edit even when enabled in config")
	fs.BoolVar(&f.ReadOnly, "read-only", false, "disable bash and write/edit even when enabled in config")
	fs.Var(&maxTurnsFlag{f: f}, "max-turns", "max agent turns per Prompt (0=unlimited)")
	fs.BoolVar(&f.NoSession, "no-session", false, "do not persist session")
	fs.StringVar(&f.SessionID, "session", "", "session id")
	fs.BoolVar(&f.Continue, "continue", false, "resume latest session")
	fs.BoolVar(&f.Stream, "stream", false, "stream token deltas")
	fs.BoolVar(&f.Verbose, "verbose", false, "debug lifecycle logs (run/tool) on stderr")
	fs.Var((*stringList)(&f.Skills), "skill", "load a named skill unconditionally regardless of selector (repeatable; use `/skill` in the TUI to list or activate)")
}

// sandboxFlagValue makes --sandbox bool-style: bare "--sandbox" means bwrap,
// and an explicit value ("--sandbox none|bwrap") still parses. IsBoolFlag
// keeps the flag package from demanding an argument for the bare form.
type sandboxFlagValue struct{ f *EngineFlags }

func (s *sandboxFlagValue) String() string {
	if s == nil || s.f == nil {
		return ""
	}
	return s.f.Sandbox
}

func (s *sandboxFlagValue) Set(v string) error {
	// The flag package passes "" for "=" forms without a value and the
	// literal "true" for the bare bool-style form; both mean "opt in".
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "true":
		s.f.Sandbox = "bwrap"
	case "false":
		s.f.Sandbox = "none"
	default:
		s.f.Sandbox = strings.TrimSpace(v) // validated in Validate()
	}
	return nil
}

// IsBoolFlag lets "--sandbox" appear without a value.
func (s *sandboxFlagValue) IsBoolFlag() bool { return true }

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
	rw, ro, writable := splitRootSpecs(f.ExtraRoots)

	// --read-only with at least one explicitly-writable (:rw) extra root:
	// the workspace and unsuffixed roots stay read-only, bash stays disabled,
	// but write/edit tools stay available and are allowed only under the :rw
	// roots (the Policy WritableRoots allowlist). Without any :rw root,
	// --read-only is a pure disable of write/edit/bash (backward compatible).
	readOnlyMode := f.ReadOnly && len(writable) > 0
	disableWrite := f.DisallowWrite
	disableShell := f.DisallowShell || f.ReadOnly
	if f.ReadOnly && !readOnlyMode {
		disableWrite = true // pure read-only: no writable roots
	}

	opt := mow.Options{
		// Host program: load $MOW_HOME + env + profiles + trust + sessions.
		// Plain mow.New (embedding) leaves LoadUserConfig false (hermetic).
		LoadUserConfig:     true,
		ConfigPaths:        paths,
		Workspace:          f.Workspace,
		ExtraRoots:         append([]string(nil), rw...),
		ExtraRootsReadOnly: append([]string(nil), ro...),
		WritableRoots:      append([]string(nil), writable...),
		ReadOnly:           readOnlyMode,
		Model:              f.Model,
		ExplicitModel:      strings.TrimSpace(f.Model) != "",
		Effort:             f.Effort,
		ExplicitEffort:     strings.TrimSpace(f.Effort) != "",
		BaseURL:            f.BaseURL,
		SystemPrefix:       append([]string(nil), f.SystemPrefix...),
		ExplicitSkills:     append([]string(nil), f.Skills...),
		AllowWrite:         f.AllowWrite,
		AllowShell:         f.AllowShell,
		Sandbox:            strings.TrimSpace(f.Sandbox),
		DisableWrite:       disableWrite,
		DisableShell:       disableShell,
		NoSession:          f.NoSession,
		SessionID:          f.SessionID,
		Continue:           f.Continue,
		Stream:             f.Stream,
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

// OptionsDeferLLM is Options with DeferLLM set so New can start without an
// API key (mow acp initialize / ping). The first Prompt still requires credentials.
func (f *EngineFlags) OptionsDeferLLM() mow.Options {
	opt := f.Options()
	opt.DeferLLM = true
	return opt
}

// Validate rejects contradictory capability flags rather than making their
// behavior depend on command-line order.
func (f *EngineFlags) Validate() error {
	if f.AllowShell && (f.DisallowShell || f.ReadOnly) {
		return fmt.Errorf("--allow-shell conflicts with --disallow-shell/--read-only")
	}
	// --allow-write with --read-only is only a conflict when there are no
	// writable (:rw) extra roots; with :rw roots, --read-only keeps write/edit
	// on but scopes them to those roots (--allow-write would re-enable writes
	// everywhere, defeating the point, so it still conflicts).
	if f.AllowWrite && (f.DisallowWrite || f.ReadOnly) {
		return fmt.Errorf("--allow-write conflicts with --disallow-write/--read-only")
	}
	// --sandbox is validated even without --allow-shell so a typo never passes
	// silently; the jail itself is a no-op until a shell exists (nothing to
	// jail), so the combination is allowed rather than rejected.
	mode, err := sandbox.ParseMode(f.Sandbox)
	if err != nil {
		return fmt.Errorf("--sandbox: %w", err)
	}
	if mode == sandbox.ModeBwrap && runtime.GOOS != "linux" {
		return fmt.Errorf("--sandbox=bwrap is Linux-only (this is %s); omit the flag or use none", runtime.GOOS)
	}
	return nil
}

// NewEngine runs BeforeNew hooks and constructs an Engine.
func (f *EngineFlags) NewEngine() (*mow.Engine, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return mow.NewHarness(f.Options())
}

// splitRootSpecs parses --extra-root values through the same suffix rules as
// policy.extra_roots (mow.SplitExtraRootSpec), so the CLI and config file can
// never drift on what ":ro" / ":rw" mean.
//
// It returns three slices:
//   - rw: unsuffixed roots ("PATH") and explicitly read-write ("PATH:rw")
//   - ro: explicitly read-only roots ("PATH:ro")
//   - writable: only the explicitly read-write ("PATH:rw") entries
//
// The writable slice is the explicit allowlist used under --read-only: there,
// unsuffixed roots become read-only (only :rw roots stay writable). Outside
// --read-only, the writable/unsuffixed distinction is irrelevant — both are
// plain read-write jail roots.
func splitRootSpecs(specs []string) (rw, ro, writable []string) {
	for _, raw := range specs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		path, readOnly := mow.SplitExtraRootSpec(raw)
		if path == "" {
			continue
		}
		if readOnly {
			ro = append(ro, path)
			continue
		}
		rw = append(rw, path)
		// Only explicitly-suffixed ":rw" entries are writable under --read-only.
		if strings.HasSuffix(strings.ToLower(raw), ":rw") {
			writable = append(writable, path)
		}
	}
	return rw, ro, writable
}
