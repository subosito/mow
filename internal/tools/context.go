package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/subosito/mow/internal/agent"
)

// contextSearchTool lets the agent query context archive files written when
// compaction dropped history. The search root is fixed by the engine to the
// mow session dir (<id>.archive subdirs) — the model supplies only patterns,
// never a path, so the tool stays safe under the default read-only policy.
//
// Results are snippets (a few lines around each hit) under a hard output cap:
// a broad pattern must never dump the archive back into the live window.
type contextSearchTool struct {
	dir string
	mu  sync.Mutex
	// retrieved bounds archive output across calls in one engine/session. A
	// search tool must not be able to refill the live context indefinitely by
	// issuing many broad queries.
	retrieved int
}

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
		"Use it after compaction to recover details no longer in context. " +
		"Results are short snippets with stable archive file:line references. " +
		"Args: pattern (string or list of strings; a line matching any of them hits), " +
		"optional max_results, optional context_lines. Pattern lists are limited to 8 entries and each pattern to 256 characters."
}
func (t *contextSearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"pattern":{"description":"fixed string, or list of fixed strings (match any)","oneOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]},` +
		`"max_results":{"type":"integer","description":"max snippets to return (default 12, capped)"},` +
		`"context_lines":{"type":"integer","description":"lines of context around each hit (default 2, capped)"}` +
		`},"required":["pattern"]}`)
}

// ReadOnly marks context_search side-effect free so read-only prompts may use it.
func (t *contextSearchTool) ReadOnly() bool { return true }

const (
	contextSearchMaxFiles    = 32 // newest archive files scanned
	contextSearchMaxReadFile = 1 << 20

	// contextSearchMaxOutput is the hard cap on the whole tool result. Kept
	// small on purpose: this tool recovers a detail, it does not re-inflate
	// the context window that compaction just reclaimed.
	contextSearchMaxOutput = 6_000

	// contextSearchMaxRetrieved is the cumulative result budget for one tool
	// instance. The per-call cap prevents one broad query; this cap prevents a
	// model from bypassing it with many broad queries in the same session.
	contextSearchMaxRetrieved = 32_000
	// Bound disk work as well as returned context. A miss should not rescan
	// dozens of megabytes on every repeated query.
	contextSearchMaxScanBytes = 8 << 20
	// Reserve room for the explicit truncation note when a call hits its cap.
	contextSearchTruncationReserve = 128

	contextSearchDefaultResults  = 12
	contextSearchMaxResults      = 25
	contextSearchMaxPatterns     = 8
	contextSearchMaxPatternRunes = 256
	contextSearchDefaultCtxLine  = 2
	contextSearchMaxCtxLines     = 5

	// contextSearchMaxLineRunes clamps one rendered line inside a snippet.
	contextSearchMaxLineRunes = 200
	// contextSearchMaxSnippet clamps one whole rendered snippet.
	contextSearchMaxSnippet = 900
)

// contextSearchArgs is decoded leniently: pattern accepts the legacy string
// form and a list form, so {"pattern":"x"} keeps working unchanged.
type contextSearchArgs struct {
	Pattern      json.RawMessage `json:"pattern"`
	MaxResults   *int            `json:"max_results"`
	ContextLines *int            `json:"context_lines"`
}

// patterns normalizes the pattern argument into a de-duplicated, non-empty
// list of fixed strings.
func (a contextSearchArgs) patterns() ([]string, error) {
	raw := bytes.TrimSpace(a.Pattern)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("context_search: pattern required")
	}
	var list []string
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("context_search: pattern: %w", err)
		}
	} else {
		var one string
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("context_search: pattern: %w", err)
		}
		list = []string{one}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, p := range list {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if len(out) >= contextSearchMaxPatterns {
			return nil, fmt.Errorf("context_search: at most %d patterns", contextSearchMaxPatterns)
		}
		if utf8.RuneCountInString(p) > contextSearchMaxPatternRunes {
			return nil, fmt.Errorf("context_search: pattern exceeds %d characters", contextSearchMaxPatternRunes)
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("context_search: pattern required")
	}
	return out, nil
}

// clampInt bounds a caller-supplied budget: the model may lower a limit but
// never raise it past the hard ceiling.
func clampInt(v *int, def, min, max int) int {
	if v == nil {
		return def
	}
	n := *v
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// searchBudget tracks what the run has spent across all archive files.
type searchBudget struct {
	results    int
	maxResults int
	maxOutput  int
	scanned    int
	maxScan    int
	stopReason string
	out        strings.Builder
	seen       map[uint64]bool // normalized snippet hashes (cross-file dedup)
	truncated  bool            // stopped early on a budget, matches remained
}

func (b *searchBudget) exhausted() bool {
	return b.results >= b.maxResults || b.out.Len() >= b.maxOutput
}

// add appends one rendered snippet unless it duplicates an earlier one or
// would break the output cap. Reports whether scanning should continue.
func (b *searchBudget) add(snippet string) bool {
	if b.exhausted() {
		b.stopReason = "result cap"
		b.truncated = true
		return false
	}
	key := snippetKey(snippet)
	if b.seen[key] {
		return true // duplicate content: skip, keep scanning
	}
	if b.out.Len()+len(snippet) > b.maxOutput {
		b.stopReason = "output cap"
		b.truncated = true
		return false
	}
	b.seen[key] = true
	b.out.WriteString(snippet)
	b.results++
	if b.exhausted() {
		b.stopReason = "result cap"
		b.truncated = true
		return false
	}
	return true
}

func (t *contextSearchTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a contextSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	patterns, err := a.patterns()
	if err != nil {
		return "", err
	}
	maxResults := clampInt(a.MaxResults, contextSearchDefaultResults, 1, contextSearchMaxResults)
	ctxLines := clampInt(a.ContextLines, contextSearchDefaultCtxLine, 0, contextSearchMaxCtxLines)

	// Serialize searches so the cumulative budget is an actual session-wide
	// limit even when the agent runs read-only tools in parallel.
	t.mu.Lock()
	defer t.mu.Unlock()

	files := t.archiveFiles()
	if len(files) == 0 {
		msg := "(no context archives yet — nothing has been compacted out of context)"
		t.retrieved += len(msg)
		return msg, nil
	}

	// Account the result before returning it. If the session budget is nearly
	// spent, return a small explicit message rather than silently consuming an
	// unbounded amount of context over repeated calls.
	remaining := contextSearchMaxRetrieved - t.retrieved
	if remaining <= 0 {
		return "(context_search retrieval budget exhausted for this session; refine the task or continue in a new session)", nil
	}
	callCap := min(contextSearchMaxOutput, max(0, remaining-contextSearchTruncationReserve))
	if callCap < 1 {
		return "(context_search retrieval budget exhausted for this session; refine the task or continue in a new session)", nil
	}

	b := &searchBudget{maxResults: maxResults, maxOutput: callCap, maxScan: contextSearchMaxScanBytes, seen: map[uint64]bool{}}
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if b.exhausted() {
			b.truncated = true
			break
		}
		if !searchArchiveFile(t.dir, rel, patterns, ctxLines, b) {
			break
		}
	}
	if b.results == 0 {
		// Even a miss has a bounded response. Charge a small diagnostic budget so
		// repeated miss queries cannot spin indefinitely for free.
		t.retrieved += contextSearchTruncationReserve
		return "(no matches in context archive)", nil
	}
	out := strings.TrimRight(b.out.String(), "\n")
	if b.truncated {
		reason := b.stopReason
		if reason == "" {
			reason = "budget"
		}
		out += fmt.Sprintf("\n\n…(stopped at %d snippets / %s; refine the pattern for more)", b.results, reason)
	}
	// Charge the actual bounded payload, including the small truncation note.
	t.retrieved += len(out)
	return out, nil
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

// searchArchiveFile greps one archive file and appends bounded snippets to b.
// Overlapping hits are merged into the preceding snippet's window rather than
// emitted twice. Reports whether scanning should continue with more files.
func searchArchiveFile(root, rel string, patterns []string, ctxLines int, b *searchBudget) bool {
	if b.scanned >= b.maxScan {
		b.stopReason = "scan cap"
		b.truncated = true
		return false
	}
	data, ok := readArchiveFile(filepath.Join(root, rel))
	if !ok {
		return true
	}
	b.scanned += len(data)
	if b.scanned > b.maxScan {
		b.stopReason = "scan cap"
		b.truncated = true
		return false
	}
	lines := strings.Split(data, "\n")
	lastEnd := -1 // last line index already covered by an emitted snippet
	for i, line := range lines {
		if !matchesAny(line, patterns) {
			continue
		}
		if i <= lastEnd {
			continue // already shown inside the previous snippet
		}
		start := max(0, i-ctxLines)
		if start <= lastEnd {
			start = lastEnd + 1
		}
		end := min(len(lines)-1, i+ctxLines)
		lastEnd = end
		if !b.add(renderSnippet(rel, lines, start, end, i)) {
			return false
		}
	}
	return true
}

// readArchiveFile reads a bounded prefix of one archive file, rejecting
// binary-ish content.
func readArchiveFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, contextSearchMaxReadFile+1))
	if err != nil || len(data) == 0 {
		return "", false
	}
	if len(data) > contextSearchMaxReadFile {
		data = data[:contextSearchMaxReadFile]
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false
	}
	return string(data), true
}

func matchesAny(line string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

// renderSnippet formats lines[start:end] with a stable "file:line" reference
// header. The matched line is marked with '>', context lines with '|'; every
// line is clamped, and the whole snippet is clamped again.
func renderSnippet(rel string, lines []string, start, end, hit int) string {
	var s strings.Builder
	fmt.Fprintf(&s, "%s:%d\n", rel, hit+1)
	for i := start; i <= end; i++ {
		mark := "|"
		if i == hit {
			mark = ">"
		}
		fmt.Fprintf(&s, "  %s %d: %s\n", mark, i+1, clampRunes(lines[i], contextSearchMaxLineRunes))
	}
	s.WriteString("\n")
	out := s.String()
	if len(out) > contextSearchMaxSnippet {
		out = clampBytes(out, contextSearchMaxSnippet) + "\n…(snippet truncated)\n\n"
	}
	return out
}

// clampRunes truncates on a rune boundary so output stays valid UTF-8.
func clampRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// clampBytes cuts at most n bytes without splitting a rune.
func clampBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// snippetKey hashes a snippet's text (whitespace-normalized, reference header
// dropped) so the same archived turn written by two compactions is reported
// once.
func snippetKey(snippet string) uint64 {
	h := fnv.New64a()
	for _, line := range strings.Split(snippet, "\n") {
		// Drop the "rel:line" header and the "  > 12: " gutter so identical
		// text at different offsets/files collapses.
		if i := strings.Index(line, ": "); i >= 0 && strings.HasPrefix(line, "  ") {
			line = line[i+2:]
		} else if !strings.HasPrefix(line, "  ") {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		h.Write([]byte(line))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
}
