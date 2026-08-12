package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
)

// Store persists goal state as one JSON file per id under Dir.
//
// Locks are keyed by the resolved directory path (not by Store value identity),
// so two Store values that share Dir still serialize correctly — callers often
// pass Store{Dir: …} by value or construct ephemeral &Store{} for DefaultDir.
type Store struct {
	// Dir defaults to $MOW_HOME/goals (see mow.Home).
	Dir string
}

// storeDirLocks serializes disk ops per goals directory.
var storeDirLocks sync.Map // string → *sync.Mutex

// defaultStore is used when Runner.Store is nil so concurrent nil-Store runners
// still share one DefaultDir lock.
var defaultStore Store

func (s *Store) lock() *sync.Mutex {
	dir := filepath.Clean(s.dir())
	v, _ := storeDirLocks.LoadOrStore(dir, new(sync.Mutex))
	return v.(*sync.Mutex)
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

// Save writes state atomically (unique temp + rename).
func (s *Store) Save(st State) error {
	if err := validateID(st.ID); err != nil {
		return err
	}
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
	return s.saveLocked(st)
}

// Load reads a goal by id. Missing file → os.ErrNotExist.
func (s *Store) Load(id string) (State, error) {
	if err := validateID(id); err != nil {
		return State{}, err
	}
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
	return s.loadLocked(id)
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
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
	if !force {
		st, err := s.loadLocked(id)
		if err == nil && st.Status == StatusRunning {
			return fmt.Errorf("%w: %s", ErrGoalRunning, id)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	path := s.path(id)
	if !pathWithinRoot(s.dir(), path) {
		return fmt.Errorf("goal: invalid goal path for id %q", id)
	}
	if err := os.Remove(path); err != nil {
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
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
	path := s.path(id)
	if !pathWithinRoot(s.dir(), path) {
		return fmt.Errorf("goal: invalid goal path for id %q", id)
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Reset clears progress so the goal can be re-run (pending, step 0).
// Keeps Goal, MaxSteps, and last Summary. Clears session, error, last_reply,
// plan item statuses (back to pending), and current_item.
func (s *Store) Reset(id string) (State, error) {
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
	st, err := s.loadLocked(id)
	if err != nil {
		return State{}, err
	}
	st.Status = StatusPending
	st.Step = 0
	st.Error = ""
	st.SessionID = ""
	st.LastReply = ""
	st.CurrentItem = ""
	for i := range st.Plan.Items {
		st.Plan.Items[i].Status = ItemPending
		st.Plan.Items[i].Note = ""
	}
	if err := s.saveLocked(st); err != nil {
		return State{}, err
	}
	return st, nil
}

// List returns all goals sorted by UpdatedAt descending.
func (s *Store) List() ([]State, error) {
	mu := s.lock()
	mu.Lock()
	defer mu.Unlock()
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
		if err := validateID(id); err != nil {
			continue
		}
		st, err := s.loadLocked(id)
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

func ensureStoreDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("goal: store dir is not a regular directory")
	}
	return nil
}

func (s *Store) loadLocked(id string) (State, error) {
	dir := s.dir()
	path := s.path(id)
	if !pathWithinRoot(dir, path) {
		return State{}, fmt.Errorf("goal: invalid goal path for id %q", id)
	}
	raw, err := readGoalJSON(path)
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("goal load %s: %w", id, err)
	}
	sanitizeState(&st)
	return st, nil
}

func (s *Store) saveLocked(st State) error {
	dir := s.dir()
	if err := ensureStoreDir(dir); err != nil {
		return err
	}
	final := s.path(st.ID)
	if !pathWithinRoot(dir, final) {
		return fmt.Errorf("goal: invalid goal path for id %q", st.ID)
	}
	sanitizeState(&st)
	st.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > maxGoalJSONBytes {
		return fmt.Errorf("goal: state for %q exceeds %d byte cap", st.ID, maxGoalJSONBytes)
	}
	raw = append(raw, '\n')
	// Unique temp name avoids clobbering a peer write if locks ever diverge
	// (and is safe under the directory mutex).
	tmp, err := os.CreateTemp(dir, st.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// readGoalJSON Lstats then opens a regular file under the size cap, re-checking
// regularity after open (mitigates replace-with-symlink races).
func readGoalJSON(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("goal load: not a regular file")
	}
	if info.Size() > maxGoalJSONBytes {
		return nil, fmt.Errorf("goal load: file too large")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("goal load: not a regular file")
	}
	if st.Size() > maxGoalJSONBytes {
		return nil, fmt.Errorf("goal load: file too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGoalJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxGoalJSONBytes {
		return nil, fmt.Errorf("goal load: file too large")
	}
	return data, nil
}
