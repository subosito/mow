package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/subosito/mow/internal/agent"
)

// contextSearchTool lets the agent query context archive files written when
// compaction dropped history. The search root is fixed by the engine to the
// mow session dir (<id>.archive subdirs) — the model supplies only a pattern,
// never a path, so the tool stays safe under the default read-only policy.
type contextSearchTool struct{ dir string }

// NewContextSearch builds context_search rooted at the session dir. Returns
// nil when dir is empty (sessions disabled).
func NewContextSearch(sessionDir string) agent.Tool {
	if strings.TrimSpace(sessionDir) == "" {
		return nil
	}
	return &contextSearchTool{dir: sessionDir}
}

func (t *contextSearchTool) Name() string { return "context_search" }
func (t *contextSearchTool) Description() string {
	return "Search archived context that compaction dropped from this session's history " +
		"(fixed-string match over the mow context archive; newest first). " +
		"Use it after compaction to recover details no longer in context. Args: pattern."
}
func (t *contextSearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"fixed string to search for"}},"required":["pattern"]}`)
}

// ReadOnly marks context_search side-effect free so read-only prompts may use it.
func (t *contextSearchTool) ReadOnly() bool { return true }

const (
	contextSearchMaxFiles    = 32 // newest archive files scanned
	contextSearchMaxMatches  = 60
	contextSearchMaxReadFile = 1 << 20
	contextSearchMaxOutput   = 24_000
)

func (t *contextSearchTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	pattern := strings.TrimSpace(a.Pattern)
	if pattern == "" {
		return "", fmt.Errorf("context_search: pattern required")
	}
	files := t.archiveFiles()
	if len(files) == 0 {
		return "(no context archives yet — nothing has been compacted out of context)", nil
	}
	var out strings.Builder
	matches := 0
	for _, rel := range files {
		if matches >= contextSearchMaxMatches || out.Len() > contextSearchMaxOutput {
			break
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		matches += searchArchiveFile(t.dir, rel, pattern, &out, contextSearchMaxMatches-matches)
	}
	if matches == 0 {
		return "(no matches in context archive)", nil
	}
	if out.Len() > contextSearchMaxOutput {
		return out.String()[:contextSearchMaxOutput] + "\n…(truncated)", nil
	}
	return out.String(), nil
}

// archiveFiles lists <dir>/*.archive/*.md newest-first (bounded), as paths
// relative to the session dir.
func (t *contextSearchTool) archiveFiles() []string {
	dirs, err := os.ReadDir(t.dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, d := range dirs {
		if !d.IsDir() || !strings.HasSuffix(d.Name(), ".archive") {
			continue
		}
		sub := filepath.Join(t.dir, d.Name())
		entries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			files = append(files, filepath.Join(d.Name(), e.Name()))
		}
	}
	// Newest first: sequence prefix sorts chronologically within a session;
	// session dirs are ordered by name (timestamp-based ids sort well too).
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) > contextSearchMaxFiles {
		files = files[:contextSearchMaxFiles]
	}
	return files
}

// searchArchiveFile greps one archive file for pattern, appending
// "file:line:text" hits to out. Returns the number of matches appended.
func searchArchiveFile(root, rel, pattern string, out *strings.Builder, budget int) int {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return 0
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, contextSearchMaxReadFile+1))
	if err != nil || len(data) == 0 {
		return 0
	}
	if len(data) > contextSearchMaxReadFile {
		data = data[:contextSearchMaxReadFile]
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return 0 // binary-ish
	}
	n := 0
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, pattern) {
			continue
		}
		fmt.Fprintf(out, "%s:%d:%s\n", rel, i+1, line)
		n++
		if n >= budget {
			out.WriteString("…(more matches; refine the pattern)\n")
			return n
		}
	}
	return n
}
