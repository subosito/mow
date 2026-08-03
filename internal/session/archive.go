package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subosito/mow/internal/llm"
)

// Context archive: when the agent loop compacts history, the engine persists
// the pre-compact messages as plain-text files here so the agent can later
// query what was dropped (context_search tool). Deliberately file/grep-based
// — no embeddings, no vector store.

const (
	// archiveMaxFileBytes caps one archive file (memory guard, not RAG).
	archiveMaxFileBytes = 1 << 20
	// archiveMaxMessageBytes caps one rendered message body inside a file.
	archiveMaxMessageBytes = 100_000
	// archiveKeepFiles bounds archive files per session (oldest dropped).
	archiveKeepFiles = 16
	// archiveArgsPreview caps rendered tool-call arguments.
	archiveArgsPreview = 300
)

// ArchiveDir is the directory holding this session's context archive files.
// It sits next to <id>.jsonl in the session dir. Empty when the store has no
// usable dir/id.
func (s *Store) ArchiveDir() string {
	if s == nil || s.Dir == "" {
		return ""
	}
	id := filepath.Base(strings.TrimSpace(s.ID))
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return filepath.Join(s.Dir, id+".archive")
}

// ArchiveCompact renders the pre-compaction messages to text and persists them
// as one archive file (bounded, oldest pruned). Returns the written path.
// Empty messages are a no-op ("", nil). Best-effort by contract: callers log
// errors but never fail a run over archiving.
func (s *Store) ArchiveCompact(messages []llm.Message, layer string, charsSaved int) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}
	dir := s.ArchiveDir()
	if dir == "" {
		return "", fmt.Errorf("session: archive dir unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, nextArchiveName(dir))

	var b strings.Builder
	fmt.Fprintf(&b, "# context archive\nsession: %s\nts: %s\nlayer: %s\nchars_saved: %d\nmessages: %d\n\n",
		s.ID, time.Now().UTC().Format(time.RFC3339), layer, charsSaved, len(messages))
	for _, m := range messages {
		writeArchiveMessage(&b, m)
		if b.Len() > archiveMaxFileBytes {
			b.WriteString("\n…(archive truncated at file cap)\n")
			break
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	pruneArchive(dir, archiveKeepFiles)
	return path, nil
}

// ArchiveFiles lists this session's archive files oldest-first (by their
// zero-padded sequence prefix). Missing dir is empty, not an error.
func (s *Store) ArchiveFiles() ([]string, error) {
	dir := s.ArchiveDir()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(dir, n)
	}
	return out, nil
}

// nextArchiveName returns NNNN-<utc-stamp>.md with NNNN one past the current
// max so lexicographic order == chronological order.
func nextArchiveName(dir string) string {
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
	return fmt.Sprintf("%04d-%s.md", maxSeq+1, time.Now().UTC().Format("20060102T150405.000"))
}

// pruneArchive deletes the oldest files beyond keep (best-effort).
func pruneArchive(dir string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names[:max(0, len(names)-keep)] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// writeArchiveMessage renders one message with a role header. Tool-call
// arguments are previewed, not dumped in full.
func writeArchiveMessage(b *strings.Builder, m llm.Message) {
	role := m.Role
	if role == "" {
		role = "?"
	}
	fmt.Fprintf(b, "## [%s]", role)
	if m.Name != "" {
		fmt.Fprintf(b, " %s", m.Name)
	}
	if m.ToolCallID != "" {
		fmt.Fprintf(b, " tool_call_id=%s", m.ToolCallID)
	}
	b.WriteString("\n")
	for _, tc := range m.ToolCalls {
		args := tc.Function.Arguments
		if len(args) > archiveArgsPreview {
			args = args[:archiveArgsPreview] + "…(truncated)"
		}
		fmt.Fprintf(b, "tool_call: %s %s\n", tc.Function.Name, args)
	}
	content := m.Content
	if len(content) > archiveMaxMessageBytes {
		content = content[:archiveMaxMessageBytes] + "\n…(message truncated)"
	}
	if content != "" {
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
}
