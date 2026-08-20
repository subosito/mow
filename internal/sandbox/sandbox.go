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

// Spec describes what the jail must expose: the workspace (always read-write)
// plus the path-jail extra roots, mirroring internal/policy.
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
	// Workspace is bound read-write and becomes the working directory.
	Workspace string
	// Roots are extra path-jail roots (read-only ones bound --ro-bind).
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
	"/nix/store", // NixOS: /bin and /usr are mostly symlinks into here
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

// Mode reports "bwrap".
func (b *Bwrap) Mode() Mode { return ModeBwrap }

// Args returns the bwrap argv prefix (everything up to and including "--").
// Exposed for tests and for `mow doctor`-style reporting.
func (b *Bwrap) Args() []string {
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
	)
	for _, p := range systemROBinds {
		if exists(p) {
			args = append(args, "--ro-bind", p, p)
		}
	}
	// Workspace read-write, then the extra roots. $HOME is NOT bound unless it
	// happens to be one of these — that is the whole point of the jail.
	args = append(args, "--bind", b.Workspace, b.Workspace)
	for _, r := range b.Roots {
		p := strings.TrimSpace(r.Path)
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		p = filepath.Clean(p)
		if p == b.Workspace || !exists(p) {
			continue
		}
		flag := "--bind"
		if r.ReadOnly {
			flag = "--ro-bind"
		}
		args = append(args, flag, p, p)
	}
	args = append(args, "--chdir", b.Workspace, "--clearenv")
	for _, k := range envAllowlist {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			args = append(args, "--setenv", k, v)
		}
	}
	return append(args, "--")
}

// Wrap rewrites cmd to run under bwrap, preserving IO, Dir, and SysProcAttr.
func (b *Bwrap) Wrap(cmd *exec.Cmd) (*exec.Cmd, error) {
	if cmd == nil {
		return nil, fmt.Errorf("sandbox: nil command")
	}
	if b == nil || strings.TrimSpace(b.Bin) == "" {
		return nil, fmt.Errorf("sandbox: bwrap backend not initialized")
	}
	argv := append(b.Args(), cmd.Args...)
	out := exec.Command(b.Bin, argv...)
	out.Dir = b.Workspace
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
	_, err := os.Lstat(p)
	return err == nil
}
