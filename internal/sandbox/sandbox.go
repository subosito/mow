// Package sandbox wraps shell commands in an optional OS jail.
//
// One backend only: bubblewrap (`bwrap`) on Linux, opt-in via --sandbox=bwrap
// (config: policy.sandbox). The default is "none" — identity, today's
// behavior: --allow-shell keeps meaning unsandboxed `bash -lc` as the user.
//
// Honest scope: this is a filesystem/home jail, not malware containment and
// not a VM. Network stays ON inside the sandbox (no --unshare-net), so
// `curl | sh` still reaches the internet and can still exfiltrate anything the
// sandbox can read. What it buys is that a runaway command cannot read
// ~/.ssh, ~/.aws, or write outside the workspace and the configured extra
// roots.
//
// Used by exactly two call sites — the bash tool (internal/tools) and
// proc_start (internal/proc) — because wrapping only one of them leaves the
// other as an escape hatch.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Mode names a sandbox backend.
type Mode string

const (
	// ModeNone runs commands directly (default; today's behavior).
	ModeNone Mode = "none"
	// ModeBwrap runs commands inside bubblewrap (Linux only).
	ModeBwrap Mode = "bwrap"
)

// ParseMode normalizes a CLI/config sandbox value. Empty and "none" mean no
// sandbox; anything other than a known backend is an error (a typo must never
// silently degrade to "unsandboxed").
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "off", "false":
		return ModeNone, nil
	case "bwrap", "bubblewrap":
		return ModeBwrap, nil
	default:
		return "", fmt.Errorf("unknown sandbox %q (want none|bwrap)", s)
	}
}

// Root is one directory tree exposed inside the sandbox.
type Root struct {
	Path     string
	ReadOnly bool
}

// Spec describes what the jail must expose: the workspace plus the path-jail
// extra roots, mirroring internal/policy. A Roots entry for the workspace
// itself carries its ReadOnly flag (policy --read-only).
type Spec struct {
	Workspace string
	Roots     []Root
}

// Backend rewrites a command so it runs inside the sandbox.
type Backend interface {
	// Wrap returns the command to actually execute. Implementations must
	// preserve Dir, Env, Stdin/Stdout/Stderr and SysProcAttr semantics of the
	// caller (process group / session handling lives with the caller).
	Wrap(cmd *exec.Cmd) (*exec.Cmd, error)
	// Mode reports the backend name (for docs, status, tool descriptions).
	Mode() Mode
}

// New builds the backend for mode. ModeNone yields an identity backend.
// ModeBwrap requires Linux and a `bwrap` binary on PATH — a missing bwrap is a
// hard error, never a silent fallback to a raw shell.
func New(mode Mode, spec Spec) (Backend, error) {
	switch mode {
	case "", ModeNone:
		return None{}, nil
	case ModeBwrap:
		if runtime.GOOS != "linux" {
			return nil, fmt.Errorf("sandbox=bwrap requires Linux (this is %s); no other sandbox backend exists", runtime.GOOS)
		}
		path, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("sandbox=bwrap: bwrap not found on PATH (install bubblewrap); refusing to run shell unsandboxed")
		}
		ws := strings.TrimSpace(spec.Workspace)
		if ws == "" {
			return nil, fmt.Errorf("sandbox=bwrap: workspace not set")
		}
		if abs, err := filepath.Abs(ws); err == nil {
			ws = abs
		}
		return &Bwrap{Bin: path, Workspace: filepath.Clean(ws), Roots: append([]Root(nil), spec.Roots...)}, nil
	default:
		return nil, fmt.Errorf("unknown sandbox %q (want none|bwrap)", string(mode))
	}
}

// None is the identity backend.
type None struct{}

// Wrap returns cmd unchanged.
func (None) Wrap(cmd *exec.Cmd) (*exec.Cmd, error) { return cmd, nil }

// Mode reports "none".
func (None) Mode() Mode { return ModeNone }

// Bwrap wraps commands in bubblewrap.
type Bwrap struct {
	// Bin is the resolved bwrap path.
	Bin string
	// Workspace is always bound (read-write unless a Roots entry marks it
	// read-only) and is the default working directory.
	Workspace string
	// Roots are the path-jail roots (read-only ones bound --ro-bind); may
	// repeat Workspace to carry its read-only flag.
	Roots []Root
	// NewSession adds --new-session (setsid inside the jail). Safe for the
	// bash tool; proc_start already calls setsid itself and tracks the pid, so
	// it leaves this off rather than detaching twice.
	NewSession bool
}

// systemROBinds is the minimal read-only system set. Missing paths are skipped
// so the same list works on NixOS, Debian, and containers.
var systemROBinds = []string{
	"/usr",
	"/lib",
	"/lib64",
	"/bin",
	"/sbin",
	"/etc/ssl",
	"/etc/ca-certificates",
	"/etc/pki",
	"/etc/resolv.conf",
	"/etc/nsswitch.conf",
	"/etc/hosts",
	"/etc/localtime",
	"/etc/passwd",
	"/etc/group",
	"/nix/store",          // NixOS: /bin and /usr are mostly symlinks into here
	"/nix/var",            // profiles, gc roots
	"/run/current-system", // NixOS system-path (bash, git, coreutils)
	"/run/wrappers",       // NixOS setuid wrappers (rarely needed; skip if missing)
}

// envAllowlist is passed through after --clearenv when set in the parent.
// Deliberately short: no tokens, no API keys, no agent-specific env. The
// toolchain cache vars are here because a Go/Rust build inside the jail with
// no cache is unusably slow — they are only forwarded when already set, and
// only useful when their directories are inside a bound root.
var envAllowlist = []string{
	"PATH",
	"HOME",
	"LANG",
	"LC_ALL",
	"TERM",
	"USER",
	"LOGNAME",
	"TZ",
	"GOCACHE",
	"GOMODCACHE",
	"GOPATH",
	"GOTOOLCHAIN",
	"CARGO_HOME",
	"RUSTUP_HOME",
}

// credentialEnv lists environment variables that must never reach a child
// shell. Suffix "_API_KEY" is dropped as well (see ScrubEnv).
var credentialEnv = map[string]bool{
	"MOW_API_KEY":       true,
	"OPENAI_API_KEY":    true,
	"ANTHROPIC_API_KEY": true,
}

// ScrubEnv returns env without the agent's own credentials: the exact names
// in credentialEnv plus any variable ending in _API_KEY. Used by the
// unsandboxed bash tool and proc_start so a child cannot read the key mow
// itself authenticates with; the bwrap path clears the environment entirely.
//
// allow lists exact variable names that survive the scrub anyway
// (policy.env_passthrough) — a host opt-out for builds that genuinely need a
// registry or CI token. Host/CLI config only; never project-settable.
func ScrubEnv(env []string, allow ...string) []string {
	keep := map[string]bool{}
	for _, name := range allow {
		if name = strings.TrimSpace(name); name != "" {
			keep[name] = true
		}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !keep[k] && (credentialEnv[k] || strings.HasSuffix(k, "_API_KEY")) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Mode reports "bwrap".
func (b *Bwrap) Mode() Mode { return ModeBwrap }

// binds returns the directories to expose, deduplicated by cleaned path and
// sorted so a parent is always bound before a child: bwrap applies binds in
// argv order, so a child bound first would be hidden by its parent's later
// mount, and the read-only/read-write choice of the more specific root would
// be lost. Read-only wins on duplicates, mirroring policy.
func (b *Bwrap) binds() []Root {
	idx := map[string]int{}
	var out []Root
	add := func(p string, ro bool) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		p = filepath.Clean(p)
		if p != b.Workspace && !exists(p) {
			return
		}
		if i, ok := idx[p]; ok {
			out[i].ReadOnly = out[i].ReadOnly || ro
			return
		}
		idx[p] = len(out)
		out = append(out, Root{Path: p, ReadOnly: ro})
	}
	add(b.Workspace, false)
	for _, r := range b.Roots {
		add(r.Path, r.ReadOnly)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// chdirFor returns the in-jail working directory: cmd.Dir when it lies under
// a bound root (a proc_start workdir), else the workspace.
func (b *Bwrap) chdirFor(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return b.Workspace
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	sep := string(filepath.Separator)
	for _, r := range b.binds() {
		if dir == r.Path || strings.HasPrefix(dir, r.Path+sep) {
			return dir
		}
	}
	return b.Workspace
}

// Args returns the bwrap argv prefix (everything up to and including "--")
// with the workspace as working directory. Exposed for tests and for
// `mow doctor`-style reporting.
func (b *Bwrap) Args() []string { return b.args(b.Workspace) }

func (b *Bwrap) args(chdir string) []string {
	args := []string{
		"--die-with-parent",
		"--unshare-pid",
	}
	if b.NewSession {
		args = append(args, "--new-session")
	}
	args = append(args,
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		// Parent dirs so --ro-bind-try /run/current-system and /nix/store
		// have somewhere to attach. Harmless on Debian (empty dirs).
		"--dir", "/run",
		"--dir", "/nix",
	)
	// --ro-bind-try: skip missing paths. Debian has /lib; NixOS often does
	// not. NixOS needs /nix/store and /run/current-system; Debian does not.
	// One list, no host-specific branches.
	for _, p := range systemROBinds {
		args = append(args, "--ro-bind-try", p, p)
	}
	// Do not also --ro-bind-try dirname(bash). On NixOS the login bash is
	// /run/current-system/sw/bin/bash; binding that directory *and*
	// /run/current-system makes bwrap `Can't mkdir /run/current-system/sw/bin`.
	// /nix/store, /run/current-system, /usr, and /bin already cover FHS and Nix.
	// Absolute argv[0] (absArgv) is enough once those parents are mounted.
	// Workspace and extra roots, parents first (see binds). $HOME is NOT
	// bound unless it happens to be one of these — that is the whole point of
	// the jail.
	for _, r := range b.binds() {
		flag := "--bind"
		if r.ReadOnly {
			flag = "--ro-bind"
		}
		args = append(args, flag, r.Path, r.Path)
	}
	args = append(args, "--chdir", chdir, "--clearenv")
	for _, k := range envAllowlist {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			args = append(args, "--setenv", k, v)
		}
	}
	return append(args, "--")
}

// Wrap rewrites cmd to run under bwrap, preserving IO and SysProcAttr. cmd.Dir
// is honoured (as --chdir) when it lies under a bound root; otherwise the
// working directory falls back to the workspace.
func (b *Bwrap) Wrap(cmd *exec.Cmd) (*exec.Cmd, error) {
	if cmd == nil {
		return nil, fmt.Errorf("sandbox: nil command")
	}
	if b == nil || strings.TrimSpace(b.Bin) == "" {
		return nil, fmt.Errorf("sandbox: bwrap backend not initialized")
	}
	dir := b.chdirFor(cmd.Dir)
	argv := append(b.args(dir), absArgv(cmd.Args)...)
	out := exec.Command(b.Bin, argv...)
	out.Dir = dir
	out.Stdin = cmd.Stdin
	out.Stdout = cmd.Stdout
	out.Stderr = cmd.Stderr
	out.SysProcAttr = cmd.SysProcAttr
	out.ExtraFiles = cmd.ExtraFiles
	// Env is rebuilt by --clearenv/--setenv inside the jail; the bwrap process
	// itself only needs PATH-free absolute argv.
	out.Env = []string{}
	return out, nil
}

// WithNewSession returns a backend that adds (or omits) bwrap's --new-session.
// The bash tool wants it: the command gets its own session, so a stray child
// cannot grab the agent's terminal. proc_start does not: it already calls
// setsid in the parent and tracks the resulting pid for status/stop, and
// detaching a second time inside the jail only muddies that. Non-bwrap
// backends are returned unchanged.
func WithNewSession(b Backend, on bool) Backend {
	bw, ok := b.(*Bwrap)
	if !ok || bw == nil {
		return b
	}
	clone := *bw
	clone.NewSession = on
	return &clone
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// absArgv rewrites argv[0] to an absolute path when LookPath can find it.
// Inside the jail PATH may still be the host PATH, but the first lookup of
// `bash` happens before /nix/store and /run/current-system are necessarily
// useful as relative names — an absolute store path is always under a bind.
func absArgv(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := append([]string(nil), args...)
	if filepath.IsAbs(out[0]) {
		return out
	}
	if p, err := exec.LookPath(out[0]); err == nil {
		out[0] = p
	}
	return out
}
