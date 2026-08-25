package session

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tool-result store: large tool bodies are persisted beside the session so
// live history can carry a small, stable reference instead of the full text.

const (
	// toolResultKeepFiles bounds stored tool bodies per session (oldest dropped).
	toolResultKeepFiles = 64
	// toolResultMaxDirBytes bounds total stored tool bodies per session.
	toolResultMaxDirBytes = 32 << 20
	// toolResultMaxReadBytes is the per-file ceiling for stored bodies: the
	// bounded read cap AND the write cap. SaveToolResult rejects anything
	// larger so a stored id can never be unretrievable.
	toolResultMaxReadBytes = 8 << 20
)

var (
	toolResultIDPattern     = regexp.MustCompile(`^[0-9]{4}-[a-z0-9_-]+-[0-9a-f]{8}\.txt$`)
	toolResultAnySeqPattern = regexp.MustCompile(`^[0-9]+-[a-z0-9_-]+-[0-9a-f]{8}\.txt$`)
)

// ToolDir is the directory holding this session's stored tool results. It sits
// next to <id>.jsonl in the session dir. Empty when the store has no usable
// dir/id.
func (s *Store) ToolDir() string {
	if s == nil || s.Dir == "" {
		return ""
	}
	id := filepath.Base(strings.TrimSpace(s.ID))
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return filepath.Join(s.Dir, id+".tools")
}

// SaveToolResult persists one full tool result and returns its stable filename.
// Empty bodies are a no-op ("", nil); bodies above toolResultMaxReadBytes are
// rejected (a stored id must always be retrievable). Saves are serialized per
// store so parallel tool batches cannot collide on sequence numbers or prune a
// just-written file. Stored results are bounded and pruned; pruning is
// best-effort and never fails an otherwise successful write.
func (s *Store) SaveToolResult(tool, body string) (string, error) {
	if body == "" {
		return "", nil
	}
	if len(body) > toolResultMaxReadBytes {
		return "", fmt.Errorf("session: tool result exceeds %d byte store cap", toolResultMaxReadBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.ensureToolDirLocked()
	if err != nil {
		return "", err
	}
	seq := nextToolSequence(dir)
	if seq > 9999 {
		// IDs intentionally have a fixed four-digit sequence. Rotate the
		// bounded cache at exhaustion rather than creating names that readers
		// and pruning do not recognize. Also removes overflow artifacts left by
		// older versions.
		resetToolResults(dir)
		seq = 1
	}
	sum := sha1.Sum([]byte(body))
	id := fmt.Sprintf("%04d-%s-%x.txt", seq, sanitizeToolName(tool), sum[:4])
	path, err := toolResultPath(dir, id)
	if err != nil {
		return "", err
	}
	f, err := createRegularFileNoFollow(dir, path, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	pruneToolResults(dir, toolResultKeepFiles, toolResultMaxDirBytes)
	return id, nil
}

// GetToolResult reads one stored tool result by its stable filename. Missing
// or pruned results fail explicitly so callers can distinguish expiration.
func (s *Store) GetToolResult(id string) (string, error) {
	if !toolResultIDPattern.MatchString(id) {
		return "", fmt.Errorf("session: invalid tool result id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.ToolDir()
	if dir == "" {
		return "", fmt.Errorf("session: tool result dir unavailable")
	}
	if _, err := os.Lstat(dir); err != nil {
		return "", fmt.Errorf("session: tool result expired or missing")
	}
	path, err := toolResultPath(dir, id)
	if err != nil {
		return "", fmt.Errorf("session: tool result expired or missing")
	}
	f, err := openRegularFileNoFollow(dir, path)
	if err != nil {
		return "", fmt.Errorf("session: tool result expired or missing")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("session: tool result expired or missing")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("session: tool result expired or missing")
	}
	if info.Size() > toolResultMaxReadBytes {
		return "", fmt.Errorf("session: tool result exceeds %d byte read limit", toolResultMaxReadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, toolResultMaxReadBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > toolResultMaxReadBytes {
		return "", fmt.Errorf("session: tool result exceeds %d byte read limit", toolResultMaxReadBytes)
	}
	return string(data), nil
}

func (s *Store) ensureToolDirLocked() (string, error) {
	dir := s.ToolDir()
	if dir == "" {
		return "", fmt.Errorf("session: tool result dir unavailable")
	}
	if s.Dir == "" {
		return "", fmt.Errorf("session: tool result dir unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := rejectSymlinkComponents(s.Dir, dir); err != nil {
		return "", fmt.Errorf("session: tool result dir is not a regular directory")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("session: tool result dir is not a regular directory")
	}
	return dir, nil
}

func sanitizeToolName(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "" {
		return "tool"
	}
	for _, r := range tool {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "tool"
		}
	}
	return tool
}

// nextToolSequence returns one past the current maximum four-digit sequence.
func nextToolSequence(dir string) int {
	maxSeq := 0
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if len(name) < 5 || name[4] != '-' {
				continue
			}
			seq := 0
			for _, r := range name[:4] {
				if r < '0' || r > '9' {
					seq = -1
					break
				}
				seq = seq*10 + int(r-'0')
			}
			if seq > maxSeq {
				maxSeq = seq
			}
		}
	}
	return maxSeq + 1
}

// pruneToolResults deletes oldest files beyond keep or the byte cap
// (best-effort).
func pruneToolResults(dir string, keep int, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type storedFile struct {
		name string
		size int64
	}
	var files []storedFile
	var total int64
	for _, e := range entries {
		if e.IsDir() || !toolResultIDPattern.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, storedFile{name: e.Name(), size: info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	remaining := len(files)
	for _, file := range files {
		if (keep <= 0 || remaining <= keep) && (maxBytes <= 0 || total <= maxBytes) {
			break
		}
		if os.Remove(filepath.Join(dir, file.name)) == nil {
			remaining--
			total -= file.size
		}
	}
}

// resetToolResults rotates the bounded result cache when its four-digit
// sequence space is exhausted. Removal is safe for symlinks: os.Remove unlinks
// the directory entry and never follows its target.
func resetToolResults(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if toolResultAnySeqPattern.MatchString(entry.Name()) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
