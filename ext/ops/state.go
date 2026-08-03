package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TickState persists per-tick cursors for `mow ops run`: log tail offsets
// (so ticks read only new bytes) and signature cooldowns (so alerts are not
// re-fired every tick). Stored as <dir>/tick-state.json.
type TickState struct {
	Services   map[string]ServiceState   `json:"services,omitempty"`
	Signatures map[string]SignatureState `json:"signatures,omitempty"`
}

// ServiceState tracks per-service log cursors.
type ServiceState struct {
	Logs map[string]LogState `json:"logs,omitempty"`
}

// LogState is where a tick stopped reading one log file. Inode detects
// rotation: a changed inode means the offset belongs to the old file.
type LogState struct {
	Offset int64  `json:"offset"`
	Inode  uint64 `json:"inode"`
}

// SignatureState tracks detections of one incident signature.
type SignatureState struct {
	LastNotified time.Time `json:"last_notified"`
	Count        int       `json:"count"`
}

const stateFileName = "tick-state.json"

// LoadState reads tick-state.json from dir. A missing file yields an empty
// state (first tick); a corrupt file is an error the caller should report.
func LoadState(dir string) (*TickState, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return &TickState{}, nil
		}
		return nil, err
	}
	var st TickState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", stateFileName, err)
	}
	return &st, nil
}

// SaveState atomically writes tick-state.json into dir (temp file in the same
// dir, then rename — same pattern as the incident store).
func SaveState(dir string, state *TickState) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, stateFileName))
}

// UpdateLogOffset records where a tick stopped reading service's log at path.
func UpdateLogOffset(state *TickState, service, path string, offset int64, inode uint64) {
	if state == nil || strings.TrimSpace(service) == "" || strings.TrimSpace(path) == "" {
		return
	}
	if state.Services == nil {
		state.Services = map[string]ServiceState{}
	}
	svc := state.Services[service]
	if svc.Logs == nil {
		svc.Logs = map[string]LogState{}
	}
	svc.Logs[path] = LogState{Offset: offset, Inode: inode}
	state.Services[service] = svc
}

// UpdateSignature records the current detection count for sig.
func UpdateSignature(state *TickState, sig string, count int) {
	if state == nil || strings.TrimSpace(sig) == "" {
		return
	}
	if state.Signatures == nil {
		state.Signatures = map[string]SignatureState{}
	}
	ss := state.Signatures[sig]
	ss.Count = count
	state.Signatures[sig] = ss
}

// MarkNotified stamps sig as notified now (call after actually alerting /
// opening the incident, so cooldown starts from the real notification).
func MarkNotified(state *TickState, sig string) {
	if state == nil || strings.TrimSpace(sig) == "" {
		return
	}
	if state.Signatures == nil {
		state.Signatures = map[string]SignatureState{}
	}
	ss := state.Signatures[sig]
	ss.LastNotified = time.Now().UTC()
	state.Signatures[sig] = ss
}

// ShouldNotify reports whether an alert for sig is due: unknown or zero-count
// signatures stay quiet; otherwise notify when never notified or when the
// cooldown since the last notification has elapsed.
func ShouldNotify(state *TickState, sig string, cooldown time.Duration) bool {
	if state == nil {
		return false
	}
	ss, ok := state.Signatures[sig]
	if !ok || ss.Count <= 0 {
		return false
	}
	if ss.LastNotified.IsZero() {
		return true
	}
	return time.Since(ss.LastNotified) >= cooldown
}
