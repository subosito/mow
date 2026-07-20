package goal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subosito/mow"
)

// Store persists goal state as one JSON file per id under Dir.
type Store struct {
	// Dir defaults to $MOW_HOME/goals (see mow.Home).
	Dir string
}

// DefaultDir is $MOW_HOME/goals.
func DefaultDir() string {
	return filepath.Join(mow.Home(), "goals")
}

func (s *Store) dir() string {
	if s != nil && strings.TrimSpace(s.Dir) != "" {
		return s.Dir
	}
	return DefaultDir()
}

// Dir returns the resolved goals directory (for error messages).
func (s *Store) DirPath() string { return s.dir() }

func (s *Store) path(id string) string {
	return filepath.Join(s.dir(), id+".json")
}

// Save writes state atomically (temp + rename).
func (s *Store) Save(st State) error {
	if err := validateID(st.ID); err != nil {
		return err
	}
	dir := s.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := s.path(st.ID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(st.ID))
}

// Load reads a goal by id. Missing file → os.ErrNotExist.
func (s *Store) Load(id string) (State, error) {
	if err := validateID(id); err != nil {
		return State{}, err
	}
	raw, err := os.ReadFile(s.path(id))
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("goal load %s: %w", id, err)
	}
	return st, nil
}

// List returns all goals sorted by UpdatedAt descending.
func (s *Store) List() ([]State, error) {
	dir := s.dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []State
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		st, err := s.Load(id)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}
