package goal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// runLockPath is goals/<id>.run.lock — cross-process exclusive run ownership.
func (s *Store) runLockPath(id string) string {
	return filepath.Join(s.dir(), id+".run.lock")
}

// ownerAlive reports whether State.RunOwner* still names a live process on this host.
func ownerAlive(st State) bool {
	if st.RunOwnerPID <= 0 {
		return false
	}
	host, _ := os.Hostname()
	if st.RunOwnerHost != "" && host != "" && st.RunOwnerHost != host {
		// Different machine: cannot probe; treat as possibly alive (do not heal).
		return true
	}
	return processAlive(st.RunOwnerPID)
}

// setRunOwner stamps the current process as the durable run owner.
func setRunOwner(st *State) {
	if st == nil {
		return
	}
	st.RunOwnerPID = os.Getpid()
	st.RunOwnerHost, _ = os.Hostname()
	st.RunLeaseAt = time.Now().UTC()
}

// clearRunOwner drops ownership fields.
func clearRunOwner(st *State) {
	if st == nil {
		return
	}
	st.RunOwnerPID = 0
	st.RunOwnerHost = ""
	st.RunLeaseAt = time.Time{}
}

// healStaleRunning converts StatusRunning → Pending when a recorded owner
// process is gone so the goal can be resumed without --force. Goals marked
// Running with no owner metadata are left as-is (legacy files; Remove still
// guards them; a new run takes the run lock and re-stamps ownership).
func healStaleRunning(st *State) bool {
	if st == nil || st.Status != StatusRunning {
		return false
	}
	if st.RunOwnerPID <= 0 {
		return false
	}
	if ownerAlive(*st) {
		return false
	}
	st.Status = StatusPending
	if strings.TrimSpace(st.Error) == "" {
		st.Error = "run interrupted (previous process exited)"
	}
	clearRunOwner(st)
	return true
}

// acquireRunExclusive takes the in-process and cross-process run locks for id.
// release must be deferred; it is idempotent.
func acquireRunExclusive(store *Store, id string) (release func(), err error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("goal: empty id")
	}
	if store == nil {
		store = &defaultStore
	}

	procRelease, err := acquireRun(id)
	if err != nil {
		return nil, err
	}

	fl, err := acquireFileLock(store.runLockPath(id), false)
	if err != nil {
		procRelease()
		if errors.Is(err, errLocked) {
			return nil, fmt.Errorf("%w: %s", ErrGoalAlreadyRunning, id)
		}
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			fl.Release()
			procRelease()
		})
	}, nil
}
