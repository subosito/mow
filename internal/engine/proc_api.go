// Re-export of internal/proc for public mow.Proc* (extensions and external
// modules cannot import internal/). ext/proc uses these.
package engine

import (
	"github.com/subosito/mow/internal/proc"
	"github.com/subosito/mow/internal/sandbox"
)

// SandboxBackend is the OS jail handed to ProcStart. Alias so public callers
// can name the type without importing internal/sandbox.
type SandboxBackend = sandbox.Backend

// ProcErrAlreadyRunning is returned by ProcStart when id is already alive.
// (Re-exported as a value, not a sentinel, so cross-module callers can
// compare with errors.Is.)
var ProcErrAlreadyRunning = proc.ErrAlreadyRunning

// ProcInfo describes one managed process.
type ProcInfo = proc.Info

// ProcSanitizeID keeps a filesystem-safe short id.
func ProcSanitizeID(id string) string { return proc.SanitizeID(id) }

// ProcStoreDir is $MOW_HOME/proc/<workspace-hash> for this workspace.
func ProcStoreDir(home, workspace string) string { return proc.StoreDir(home, workspace) }

// ProcStart launches a detached process under dir. See internal/proc.Start.
//
// box is the OS jail for the spawned process. It is variadic to match
// proc.Start, which makes it easy to omit by accident: a caller that drops it
// gets an unsandboxed process with no compile error. proc_start is a shell
// entry point, so an unwrapped launch makes --sandbox theater. Pass the
// engine's ShellSandbox() unless you deliberately want no jail.
func ProcStart(dir, id, command, logName, workdir string, box ...sandbox.Backend) (ProcInfo, error) {
	return proc.Start(dir, id, command, logName, workdir, box...)
}

// ProcStartEnv is ProcStart with the host's policy.env_passthrough names: the
// exact environment variables allowed past the credential scrub. Use
// Engine.EnvPassthrough() for the list; ProcStart keeps the full scrub.
func ProcStartEnv(dir, id, command, logName, workdir string, envAllow []string, box ...sandbox.Backend) (ProcInfo, error) {
	return proc.StartEnv(dir, id, command, logName, workdir, nil, envAllow, box...)
}

// ProcStartScrubbed is ProcStartEnv plus envDeny: the engine's own credential
// names (Engine.CredentialEnvNames). env_passthrough still wins over it.
func ProcStartScrubbed(dir, id, command, logName, workdir string, envDeny, envAllow []string, box ...sandbox.Backend) (ProcInfo, error) {
	return proc.StartEnv(dir, id, command, logName, workdir, envDeny, envAllow, box...)
}

// ProcStatus returns the info for one id.
func ProcStatus(dir, id string) (ProcInfo, error) { return proc.Status(dir, id) }

// ProcList returns every recorded process in dir.
func ProcList(dir string) ([]ProcInfo, error) { return proc.List(dir) }

// ProcStop SIGTERMs then SIGKILLs id and removes its pid file.
func ProcStop(dir, id string) (ProcInfo, error) { return proc.Stop(dir, id) }

// ProcTail returns the last n lines of id's log.
func ProcTail(dir, id string, n int) (string, error) { return proc.Tail(dir, id, n) }
