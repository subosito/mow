package goal

import (
	"encoding/json"
	"errors"
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

// DirPath returns the resolved goals directory (for error messages).
func (s *Store) DirPath() string { return s.dir() }

// Path returns the JSON file path for a goal id (for operator hints).
func (s *Store) Path(id string) string {
	return s.path(id)
}

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

// ErrGoalRunning is returned by Remove when a goal has StatusRunning and
// force is false. Callers may retry with force=true to delete anyway.
var ErrGoalRunning = errors.New("goal is running; stop it first or use force")

// ErrGoalNotFound is returned by Remove when no goal file exists for id.
// (Delete keeps its legacy "missing file is not an error" semantics.)
var ErrGoalNotFound = errors.New("goal not found")

// Remove deletes a goal by id. A running goal is refused unless force is true.
// Other statuses (blocked, partial, failed, done) are removed: blocked is not
// actively executing, so the file is safe to delete, but callers should prompt
// the operator to prune any leftover worktree separately. A missing goal file
// is an error (use Delete for the legacy not-found-is-noop behavior).
func (s *Store) Remove(id string, force bool) error {
	if err := validateID(id); err != nil {
		return err
	}
	if !force {
		if st, err := s.Load(id); err == nil && st.Status == StatusRunning {
			return fmt.Errorf("%w: %s", ErrGoalRunning, id)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(s.path(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrGoalNotFound, id)
		}
		return err
	}
	return nil
}

// Delete removes a goal file by id. Missing file is not an error (legacy).
// Deprecated: prefer Remove, which guards against deleting running goals.
func (s *Store) Delete(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	err := os.Remove(s.path(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Reset clears progress so the goal can be re-run (pending, step 0).
// Keeps Goal, MaxSteps, and last Summary. Clears session, error, last_reply,
// plan item statuses (back to pending), and current_item.
func (s *Store) Reset(id string) (State, error) {
	st, err := s.Load(id)
	if err != nil {
		return State{}, err
	}
	st.Status = StatusPending
	st.Step = 0
	st.Error = ""
	st.SessionID = ""
	st.LastReply = ""
	st.CurrentItem = ""
	// Reset checklist statuses but keep item titles/ids.
	for i := range st.Plan.Items {
		st.Plan.Items[i].Status = ItemPending
		st.Plan.Items[i].Note = ""
	}
	// keep Summary as last successful result until overwritten
	if err := s.Save(st); err != nil {
		return State{}, err
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

// RecentFacts returns newest cross-run evidence for exactly workspace.
// Legacy states without a workspace remain isolated. Claims are deduplicated
// case-insensitively, keeping the newest occurrence, and the result is bounded.
func (s *Store) RecentFacts(workspace string, limit int) ([]Fact, error) {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "." || workspace == "" || limit <= 0 {
		return nil, nil
	}
	states, err := s.List()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]Fact, 0, min(limit, len(states)))
	for _, st := range states {
		if filepath.Clean(st.Workspace) != workspace {
			continue
		}
		for i := len(st.Facts) - 1; i >= 0; i-- {
			f := st.Facts[i]
			key := strings.ToLower(strings.TrimSpace(f.Claim))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, f)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}
