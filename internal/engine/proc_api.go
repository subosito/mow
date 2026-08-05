// Re-export of internal/proc for packs/ (which lives in a separate Go module
// and cannot import internal/). Packs that need process management —
// packs/goal's process tools — import mow.Proc* instead of internal/proc.
//
// ext/proc (which stays in the root module) imports internal/proc directly.
package engine

import "github.com/subosito/mow/internal/proc"

// ProcErrAlreadyRunning is returned by ProcStart when id is already alive.
// (Re-exported as a value, not a sentinel, so cross-module callers can
// compare with errors.Is.)
var ProcErrAlreadyRunning = proc.ErrAlreadyRunning

// ProcInfo describes one managed process.
type ProcInfo = proc.Info

// ProcSanitizeID keeps a filesystem-safe short id.
func ProcSanitizeID(id string) string { return proc.SanitizeID(id) }

// ProcStart launches a detached process under dir. See internal/proc.Start.
func ProcStart(dir, id, command, logName, workdir string) (ProcInfo, error) {
	return proc.Start(dir, id, command, logName, workdir)
}

// ProcStatus returns the info for one id.
func ProcStatus(dir, id string) (ProcInfo, error) { return proc.Status(dir, id) }

// ProcList returns every recorded process in dir.
func ProcList(dir string) ([]ProcInfo, error) { return proc.List(dir) }

// ProcStop SIGTERMs then SIGKILLs id and removes its pid file.
func ProcStop(dir, id string) (ProcInfo, error) { return proc.Stop(dir, id) }

// ProcTail returns the last n lines of id's log.
func ProcTail(dir, id string, n int) (string, error) { return proc.Tail(dir, id, n) }
