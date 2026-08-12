package goal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxEventsJSONLBytes rotates the event log when exceeded (keeps last segment).
const maxEventsJSONLBytes = 4 << 20 // 4 MiB

// LogEvent is one line in goals/<id>/events.jsonl.
type LogEvent struct {
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"` // start|step|done|fail|retry|budget
	Step    int       `json:"step,omitempty"`
	Text    string    `json:"text,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	Error   string    `json:"error,omitempty"`
	Status  Status    `json:"status,omitempty"`
	Plan    *Plan     `json:"plan,omitempty"`
}

// eventsPath is $dir/<id>/events.jsonl (alongside <id>.json).
func (s *Store) eventsPath(id string) string {
	return filepath.Join(s.dir(), id, "events.jsonl")
}

// AppendEvent writes one JSONL event for the goal (best-effort; never fails the run).
//
// The id is validated exactly like Save/Load/Delete: this is a public method on
// an exported Store, so it must not be the one path that trusts its caller. An
// id such as "../../x" would otherwise resolve outside the goals directory via
// filepath.Join, and "best-effort, never fails" would keep that silent.
//
// When the log exceeds maxEventsJSONLBytes it is rotated to events.jsonl.1
// (previous .1 discarded) so disk use stays bounded. A cross-process flock on
// events.jsonl.lock serializes append and rotation with other mow processes.
func (s *Store) AppendEvent(id string, ev LogEvent) {
	if s == nil {
		return
	}
	if err := validateID(strings.TrimSpace(id)); err != nil {
		return
	}
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	// Cap event text fields so a noisy step cannot fill disk unbounded.
	ev.Text = truncateRunes(ev.Text, 4000)
	ev.Error = truncateRunes(ev.Error, 4000)
	ev.Outcome = truncateRunes(ev.Outcome, 512)
	dir := filepath.Join(s.dir(), id)
	if !pathWithinRoot(s.dir(), dir) {
		return
	}
	eventsFile := s.eventsPath(id)
	if !pathWithinRoot(s.dir(), eventsFile) {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	fl, err := acquireFileLock(eventsFile+".lock", true)
	if err != nil {
		return
	}
	defer fl.Release()
	// Refuse to append through a symlink (planted event log).
	// Rotate before open when the active segment is already at/over the cap so
	// the new event lands in a fresh events.jsonl (not only in the .1 segment)
	// and readers always find an active file after AppendEvent returns.
	if info, err := os.Lstat(eventsFile); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return
		}
		if info.Size() >= maxEventsJSONLBytes {
			rotateEventsFile(eventsFile)
		}
	}
	f, err := openAppendRegular(eventsFile)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	_ = enc.Encode(ev)
	_ = f.Close()
}

// rotateEventsFile renames events.jsonl → events.jsonl.1 (replacing any prior
// .1) so only two generations are retained. The active path is left absent;
// the caller recreates it on the next OpenFile(O_CREATE).
func rotateEventsFile(eventsFile string) {
	prev := eventsFile + ".1"
	_ = os.Remove(prev)
	_ = os.Rename(eventsFile, prev)
}
