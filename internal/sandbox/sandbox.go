// Package sandbox wraps shell commands in an optional Fence OS sandbox.
// Both bash and proc_start use the same host-owned policy. The default is none;
// --sandbox selects Fence on Linux/macOS with denied outbound networking.
package sandbox

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Mode names a sandbox backend.
type Mode string

const (
	// ModeNone runs commands directly (default; today's behavior).
	ModeNone Mode = "none"
	// ModeFence applies a generated, offline Fence policy.
	ModeFence Mode = "fence"
)

// ParseMode normalizes a CLI/config sandbox value. Empty and "none" mean no
// sandbox; anything other than a known backend is an error (a typo must never
// silently degrade to "unsandboxed").
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "off", "false":
		return ModeNone, nil
	case "fence":
		return ModeFence, nil
	case "bwrap", "bubblewrap":
		return "", fmt.Errorf("sandbox %q was removed; use fence (requires the Fence CLI)", s)
	default:
		return "", fmt.Errorf("unknown sandbox %q (want none|fence)", s)
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

// New builds the requested backend. Missing Fence or required isolation is
// a hard error, never a fallback to a raw shell.
func New(mode Mode, spec Spec) (Backend, error) {
	switch mode {
	case "", ModeNone:
		return None{}, nil
	case ModeFence:
		return newFence(spec)
	default:
		return nil, fmt.Errorf("unknown sandbox %q (want none|fence)", mode)
	}
}

// None is the identity backend.
type None struct{}

// Wrap returns cmd unchanged.
func (None) Wrap(cmd *exec.Cmd) (*exec.Cmd, error) { return cmd, nil }

// Mode reports "none".
func (None) Mode() Mode { return ModeNone }

// rootLayout normalizes host roots and selects permitted working directories.
type rootLayout struct {
	Workspace string
	Roots     []Root
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

// envAllowlist is the small environment surface forwarded to Fence.
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

// credentialSuffixes are the variable-name shapes treated as secrets. _API_KEY
// is mow's own convention; the rest cover cloud/CI credentials a child would
// otherwise inherit from the operator's shell (AWS_SESSION_TOKEN,
// AWS_SECRET_ACCESS_KEY, GITHUB_TOKEN, …).
var credentialSuffixes = []string{
	"_API_KEY",
	"_TOKEN",
	"_SECRET",
	"_SECRET_KEY",
	"_SECRET_ACCESS_KEY",
}

// credentialExact are exact names that do not carry a telling suffix.
var credentialExact = map[string]bool{
	"AWS_SESSION_TOKEN": true,
}

// IsCredentialName reports whether a variable name looks like a credential a
// child process must not inherit. Exact names first, then suffixes; the match
// is case-sensitive like the environment itself.
func IsCredentialName(name string) bool {
	if credentialExact[name] {
		return true
	}
	for _, suf := range credentialSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// ScrubEnv returns env without the agent's own credentials. Used by the
// unsandboxed shell entry points (bash, proc_start, ACP terminal) so a child
// cannot read a key mow itself authenticates with; Fence uses a stricter
// environment allowlist.
//
// allow lists exact variable names that survive the scrub anyway
// (policy.env_passthrough) — a host opt-out for builds that genuinely need a
// registry or CI token. Host/CLI config only; never project-settable.
func ScrubEnv(env []string, allow ...string) []string {
	return ScrubEnvWith(env, nil, allow)
}

// ScrubEnvWith is ScrubEnv with an extra deny list: exact variable names that
// must never reach the child even though their spelling is not secret-shaped.
// Callers pass the model credentials mow itself was configured to use
// (config LLM.APIKeyEnv plus provider api_key_env, via Engine.CredentialEnvNames)
// — a gateway key named MY_GATEWAY_KEY would otherwise sail through.
//
// deny is applied before allow: env_passthrough (a host decision) wins.
func ScrubEnvWith(env, deny, allow []string) []string {
	keep := map[string]bool{}
	for _, name := range allow {
		if name = strings.TrimSpace(name); name != "" {
			keep[name] = true
		}
	}
	drop := map[string]bool{}
	for _, name := range deny {
		if name = strings.TrimSpace(name); name != "" {
			drop[name] = true
		}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if keep[k] {
			out = append(out, kv)
			continue
		}
		if drop[k] || IsCredentialName(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// binds deduplicates roots; read-only wins on identical paths.
func (b *rootLayout) binds() []Root {
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
	slices.SortFunc(out, func(a, b Root) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

// chdirFor returns the in-jail working directory: cmd.Dir when it lies under
// a bound root (a proc_start workdir), else the workspace.
func (b *rootLayout) chdirFor(dir string) string {
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
