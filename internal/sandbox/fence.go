package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Fence uses explicit generated settings, never repository/user auto-discovery.
// No persistent settings files or additional Go dependencies are needed.
type Fence struct {
	Bin, Shell string
	layout     rootLayout
}

func newFence(spec Spec) (Backend, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("sandbox=fence requires Linux or macOS")
	}
	bin, err := exec.LookPath("fence")
	if err != nil {
		return nil, fmt.Errorf("sandbox=fence: fence not found on PATH; refusing to run shell unsandboxed")
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("sandbox=fence: bash not found: %w", err)
	}
	if strings.TrimSpace(spec.Workspace) == "" {
		return nil, fmt.Errorf("sandbox=fence: workspace not set")
	}
	ws, err := filepath.Abs(spec.Workspace)
	if err != nil {
		return nil, err
	}
	// Fence can otherwise fall back to proxy-only filtering. Prove the network
	// namespace works before allowing this offline backend to run any command.
	if runtime.GOOS == "linux" {
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("sandbox=fence: required bwrap unavailable: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, bwrap, "--unshare-net", "--ro-bind", "/", "/", "--", shell, "--noprofile", "--norc", "-c", "exit 0")
		probe.Env = fenceEnv(os.Environ())
		if err := probe.Run(); err != nil {
			return nil, fmt.Errorf("sandbox=fence: network isolation unavailable; refusing proxy-only fallback: %w", err)
		}
	}
	return &Fence{Bin: bin, Shell: shell, layout: rootLayout{Workspace: filepath.Clean(ws), Roots: append([]Root(nil), spec.Roots...)}}, nil
}

func (*Fence) Mode() Mode { return ModeFence }

func (f *Fence) settings() ([]byte, error) {
	read := []string{}
	write := []string{}
	denyWrite := []string{}
	for _, path := range systemROBinds {
		if exists(path) {
			read = append(read, path)
		}
	}
	if runtime.GOOS == "darwin" {
		// Do not allow all of /System: /System/Volumes/Data aliases user data.
		read = append(read, "/System/Library")
	}
	for _, r := range f.layout.binds() {
		// Fence interprets paths as glob patterns; reject rather than accidentally
		// grant access to sibling paths through a metacharacter in a root name.
		if strings.ContainsAny(r.Path, "*?[]{}\\") {
			return nil, fmt.Errorf("sandbox=fence: root contains unsupported glob characters: %q", r.Path)
		}
		read = append(read, r.Path)
		if r.ReadOnly {
			denyWrite = append(denyWrite, r.Path)
		} else {
			write = append(write, r.Path)
		}
	}
	return json.Marshal(map[string]any{
		"network":    map[string]any{"allowedDomains": []string{}, "allowLocalBinding": false, "allowLocalOutbound": false},
		"filesystem": map[string]any{"strictDenyRead": true, "allowRead": read, "allowWrite": write, "denyWrite": denyWrite},
		// Retain OS/file/network containment without executable-name heuristics:
		// Nix multicall binaries otherwise make denying chroot also deny cat.
		"command":  map[string]any{"useDefaults": false},
		"devices":  map[string]any{"mode": "minimal"},
		"allowPty": false,
	})
}

// An allowlist also prevents inherited BASH_ENV or Fence helper variables from
// changing the trusted launcher. Only explicitly allowed runtime variables are forwarded.
func fenceEnv(env []string) []string {
	out := []string{}
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		for _, allowed := range envAllowlist {
			if key == allowed {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

func (f *Fence) Wrap(cmd *exec.Cmd) (*exec.Cmd, error) {
	if cmd == nil {
		return nil, fmt.Errorf("sandbox: nil command")
	}
	if f == nil || f.Bin == "" || f.Shell == "" {
		return nil, fmt.Errorf("sandbox: fence backend not initialized")
	}
	settings, err := f.settings()
	if err != nil {
		return nil, err
	}
	// Process substitution supplies a read-only settings descriptor without
	// consuming stdin, creating a policy file, or interpolating shell input.
	args := []string{"--noprofile", "--norc", "-c", `exec "$2" --settings <(printf '%s' "$1") -- "${@:3}"`, "mow-fence", string(settings), f.Bin}
	args = append(args, absArgv(cmd.Args)...)
	out := exec.Command(f.Shell, args...)
	out.Dir = f.layout.chdirFor(cmd.Dir)
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	out.Env = fenceEnv(env)
	out.Stdin, out.Stdout, out.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	out.SysProcAttr, out.ExtraFiles = cmd.SysProcAttr, cmd.ExtraFiles
	out.WaitDelay = cmd.WaitDelay
	return out, nil
}
