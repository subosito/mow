package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
)

const maxStateBytes = 2 << 20

// Store is a local JSON store. Dir defaults to $MOW_HOME/workflows/runs.
// The directory is host-owned state, not a workspace-controlled location.
type Store struct{ Dir string }

var storeLocks sync.Map // canonical directory + run id -> *sync.Mutex

func (s *Store) dir() string {
	d := strings.TrimSpace(s.Dir)
	if d == "" {
		d = filepath.Join(mow.Home(), "workflows", "runs")
	}
	if a, err := filepath.Abs(d); err == nil {
		return filepath.Clean(a)
	}
	return filepath.Clean(d)
}

func (s *Store) Path(id string) string { return filepath.Join(s.dir(), id+".json") }
func (s *Store) DirPath() string       { return s.dir() }

// List returns durable runs newest first. Lock files and temp files are ignored.
func (s *Store) List() ([]State, error) {
	dir := s.dir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []State
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if validateID(id) != nil {
			continue
		}
		st, err := s.Load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b State) int {
		if a.UpdatedAt.After(b.UpdatedAt) {
			return -1
		}
		if a.UpdatedAt.Before(b.UpdatedAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// lock combines an in-process mutex with an OS advisory lock. The file remains
// after release, but the kernel lock is released automatically on crash, so a
// stale path never prevents reconciliation.
func (s *Store) lock(id string) (func(), error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	d := s.dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, err
	}
	v, _ := storeLocks.LoadOrStore(d+"\x00"+id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	file, err := os.OpenFile(s.Path(id)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	if err := lockRunFile(file); err != nil {
		_ = file.Close()
		mu.Unlock()
		if errors.Is(err, errRunLocked) {
			return nil, ErrRunning
		}
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = file.Close() // close releases the advisory lock
			mu.Unlock()
		})
	}, nil
}

func (s *Store) load(id string) (State, error) {
	if err := validateID(id); err != nil {
		return State{}, err
	}
	file, err := os.Open(s.Path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, ErrNotFound
		}
		return State{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil || len(raw) > maxStateBytes {
		return State{}, fmt.Errorf("%w: state unreadable or oversized", ErrInvalid)
	}
	var st State
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return State{}, fmt.Errorf("%w: invalid state JSON", ErrInvalid)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return State{}, fmt.Errorf("%w: trailing state data", ErrInvalid)
	}
	if err := validateState(st); err != nil || st.ID != id {
		return State{}, fmt.Errorf("%w: invalid state", ErrInvalid)
	}
	return st, nil
}

func (s *Store) Load(id string) (State, error) {
	unlock, err := s.lock(id)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	return s.load(id)
}

func (s *Store) save(st State) error {
	if st.CreatedAt.IsZero() {
		st.CreatedAt = time.Now().UTC()
	}
	st.UpdatedAt = time.Now().UTC()
	if err := validateState(st); err != nil {
		return err
	}
	dir := s.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxStateBytes {
		return fmt.Errorf("%w: state too large", ErrInvalid)
	}
	file, err := os.CreateTemp(dir, ".workflow-*.tmp")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(path, s.Path(st.ID)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) Save(st State) error {
	unlock, err := s.lock(st.ID)
	if err != nil {
		return err
	}
	defer unlock()
	return s.save(st)
}

type State struct {
	ID           string          `json:"id"`
	Definition   string          `json:"definition"`
	Version      string          `json:"version"`
	Status       Status          `json:"status"`
	CurrentPhase string          `json:"current_phase,omitempty"`
	Input        json.RawMessage `json:"input"`
	Phases       []PhaseState    `json:"phases"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type PhaseState struct {
	ID        string          `json:"id"`
	Kind      PhaseKind       `json:"kind"`
	Status    PhaseStatus     `json:"status"`
	Attempts  int             `json:"attempts"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at"`
	Output    json.RawMessage `json:"output,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
}

func validateState(st State) error {
	if validateID(st.ID) != nil || validateID(st.Definition) != nil || validateID(st.Version) != nil || len(st.Phases) == 0 || len(st.Phases) > maxPhases || len(st.Input) == 0 || len(st.Input) > maxStateBytes || !json.Valid(st.Input) {
		return fmt.Errorf("%w: invalid workflow state", ErrInvalid)
	}
	switch st.Status {
	case StatusPending, StatusRunning, StatusSucceeded, StatusFailed, StatusBlocked:
	default:
		return fmt.Errorf("%w: invalid run status", ErrInvalid)
	}
	seen := map[string]bool{}
	running := 0
	for _, phase := range st.Phases {
		if validateID(phase.ID) != nil || seen[phase.ID] || phase.Attempts < 0 || len(phase.Output) > DefaultMaxJSONBytes || len(phase.Output) > 0 && !json.Valid(phase.Output) {
			return fmt.Errorf("%w: invalid phase state", ErrInvalid)
		}
		seen[phase.ID] = true
		switch phase.Kind {
		case PhasePrompt, PhaseVerify:
		default:
			return fmt.Errorf("%w: invalid phase kind", ErrInvalid)
		}
		switch phase.Status {
		case PhasePending, PhaseRunning, PhaseSucceeded, PhaseFailed, PhaseBlocked:
		default:
			return fmt.Errorf("%w: invalid phase status", ErrInvalid)
		}
		if phase.Status == PhaseRunning {
			running++
		}
	}
	if running > 1 || st.Status == StatusRunning && running != 1 || st.Status != StatusRunning && running != 0 {
		return fmt.Errorf("%w: inconsistent running state", ErrInvalid)
	}
	return nil
}
