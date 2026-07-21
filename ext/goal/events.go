package goal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogEvent is one line in goals/<id>/events.jsonl.
type LogEvent struct {
	TS      time.Time   `json:"ts"`
	Kind    string      `json:"kind"` // start|step|done|fail|retry|budget
	Step    int         `json:"step,omitempty"`
	Text    string      `json:"text,omitempty"`
	Outcome string      `json:"outcome,omitempty"`
	Error   string      `json:"error,omitempty"`
	Status  Status      `json:"status,omitempty"`
	Plan    *Plan       `json:"plan,omitempty"`
}

// eventsPath is $dir/<id>/events.jsonl (alongside <id>.json).
func (s *Store) eventsPath(id string) string {
	return filepath.Join(s.dir(), id, "events.jsonl")
}

// AppendEvent writes one JSONL event for the goal (best-effort; never fails the run).
func (s *Store) AppendEvent(id string, ev LogEvent) {
	if s == nil || strings.TrimSpace(id) == "" {
		return
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	dir := filepath.Join(s.dir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(s.eventsPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(ev)
}
