package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/subosito/mow"
)

const (
	maxStateJSONBytes = 16 << 10
	maxStateID        = 128
)

// TickState is the last-known fire for one job id ($MOW_HOME/job/state/<id>.json).
// Survives daemon restarts so `mow job list` / `mow ops status` can show it.
type TickState struct {
	ID         string    `json:"id"`
	LastStart  time.Time `json:"last_start,omitempty"`
	LastEnd    time.Time `json:"last_end,omitempty"`
	LastStatus string    `json:"last_status,omitempty"` // running, ok, error, skip, blocked
	LastError  string    `json:"last_error,omitempty"`
	SkipCount  int       `json:"skip_count,omitempty"` // consecutive overlap skips
	SkipTotal  int       `json:"skip_total,omitempty"`
	FireCount  int       `json:"fire_count,omitempty"`
	Updated    time.Time `json:"updated"`
}

var stateMu sync.Mutex

// DefaultStateDir is $MOW_HOME/job/state.
func DefaultStateDir() string {
	return filepath.Join(mow.Home(), "job", "state")
}

// StatePath is the JSON file for one job id.
func StatePath(id string) string {
	return filepath.Join(DefaultStateDir(), sanitizeStateID(id)+".json")
}

func sanitizeStateID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			if unicode.IsSpace(r) {
				b.WriteByte('_')
			} else {
				b.WriteByte('_')
			}
		}
		if b.Len() >= maxStateID {
			break
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// LoadTick reads persisted state for id. Missing file is a zero TickState, nil error.
func LoadTick(id string) (TickState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return TickState{}, fmt.Errorf("job: empty id")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	return loadTickLocked(id)
}

func loadTickLocked(id string) (TickState, error) {
	path := StatePath(id)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TickState{ID: id}, nil
		}
		return TickState{}, err
	}
	if len(raw) > maxStateJSONBytes {
		return TickState{}, fmt.Errorf("job: state file exceeds %d bytes", maxStateJSONBytes)
	}
	var st TickState
	if err := json.Unmarshal(raw, &st); err != nil {
		return TickState{}, err
	}
	if strings.TrimSpace(st.ID) == "" {
		st.ID = id
	}
	return st, nil
}

// SaveTick writes st atomically under DefaultStateDir.
func SaveTick(st TickState) error {
	st.ID = strings.TrimSpace(st.ID)
	if st.ID == "" {
		return fmt.Errorf("job: empty id")
	}
	if st.Updated.IsZero() {
		st.Updated = time.Now().UTC()
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	return saveTickLocked(st)
}

func saveTickLocked(st TickState) error {
	dir := DefaultStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := StatePath(st.ID)
	tmp, err := os.CreateTemp(dir, ".tick-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	if cerr != nil {
		_ = os.Remove(tmpName)
		return cerr
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// FormatTick is a one-line hint for CLI tables (empty if never fired).
func FormatTick(st TickState) string {
	if st.LastStart.IsZero() && st.LastEnd.IsZero() && st.LastStatus == "" {
		return "-"
	}
	when := st.LastEnd
	if when.IsZero() {
		when = st.LastStart
	}
	var b strings.Builder
	if !when.IsZero() {
		b.WriteString(when.Local().Format(time.RFC3339))
		b.WriteByte(' ')
	}
	if st.LastStatus != "" {
		b.WriteString(st.LastStatus)
	}
	if st.SkipCount > 0 {
		fmt.Fprintf(&b, " skip=%d", st.SkipCount)
	}
	if st.LastError != "" && st.LastStatus != "ok" {
		b.WriteByte(' ')
		err := st.LastError
		if len(err) > 40 {
			err = err[:40] + "…"
		}
		b.WriteString(err)
	}
	return strings.TrimSpace(b.String())
}

func recordTickStart(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	now := time.Now().UTC()
	stateMu.Lock()
	defer stateMu.Unlock()
	st, _ := loadTickLocked(id)
	st.ID = id
	st.LastStart = now
	st.LastStatus = "running"
	st.LastError = ""
	st.Updated = now
	_ = saveTickLocked(st)
}

func recordTickEnd(id, status, errMsg string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	now := time.Now().UTC()
	stateMu.Lock()
	defer stateMu.Unlock()
	st, _ := loadTickLocked(id)
	st.ID = id
	st.LastEnd = now
	st.LastStatus = status
	st.LastError = strings.TrimSpace(errMsg)
	if status == "ok" || status == "error" {
		st.FireCount++
		st.SkipCount = 0
	}
	st.Updated = now
	_ = saveTickLocked(st)
}

func recordTickSkip(id, reason string) TickState {
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	stateMu.Lock()
	defer stateMu.Unlock()
	st, _ := loadTickLocked(id)
	st.ID = id
	st.LastStatus = "skip"
	st.LastError = reason
	st.SkipCount++
	st.SkipTotal++
	st.Updated = now
	_ = saveTickLocked(st)
	return st
}
