package contextsink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/subosito/mow"
)

// contextSearchTool is the recovery side of the tool-result side channel: it
// searches the *current session's* compaction archives and stored tool
// results, and fetches stored results by id (the id the sink stub prints).
// The search root is resolved from the engine at call time and pinned to the
// engine's own session (SessionDir + SessionID) — the model supplies only
// patterns and ids, never a path, so the tool stays safe under the default
// read-only policy and can never read another session's data.
//
// Results are snippets (a few lines around each hit) under a hard output cap:
// a broad pattern must never dump the archive back into the live window.
type contextSearchTool struct {
	mu sync.Mutex
	// dir/sid pin the session to search (tests). Empty dir → resolve both
	// from the engine at Exec time.
	dir string
	sid string
	// retrieved bounds archive output across calls in one session. A search
	// tool must not be able to refill the live context indefinitely by
	// issuing many broad queries. Keyed per session (dir+"/"+sid) so
	// concurrent engines/sessions never share a budget, and bounded so a
	// long-lived host does not leak budget entries for dead sessions.
	retrieved map[string]int
}

// newContextSearchTool builds a tool pinned to a session dir (used by tests;
// the registered instance resolves the session from the engine instead).
func newContextSearchTool(dir, sid string) *contextSearchTool {
	return &contextSearchTool{dir: dir, sid: sid, retrieved: map[string]int{}}
}

// contextSearchToolIDPattern mirrors the stored-tool-result filename shape
// used by session.Store.SaveToolResult (<NNNN>-<tool>-<8hex>.txt).
var contextSearchToolIDPattern = regexp.MustCompile(`^\d{4}-[a-z0-9_-]+-[0-9a-f]{8}\.txt$`)

func (t *contextSearchTool) Name() string { return "context_search" }

// Enabled keeps the recovery tool out of the engine tool list unless the
// contextsink feature is explicitly enabled for that engine.
func (t *contextSearchTool) Enabled(eng *mow.Engine) bool { return loadConfig(eng).Enabled }
func (t *contextSearchTool) Description() string {
	return "Search archived context that compaction dropped from this session's history " +
		"(fixed-string match over the mow context archive; newest first). " +
		"Use it after compaction to recover details no longer in context. " +
		"Results are short snippets with stable archive file:line references. " +
		"Args: pattern (string or list of strings; a line matching any of them hits), " +
		"optional max_results, optional context_lines. Pattern lists are limited to 8 entries and each pattern to 256 characters. " +
		"Search also covers stored tool results (their snippet headers carry a 'stored ' prefix). " +
		"Or fetch one stored result by id: Args: id (stored tool result id, e.g. 0003-bash-ab12cd34.txt) with " +
		"offset (rune offset into the body, default 0) and window (chars to return, default 4000, clamped); " +
		"get-by-id returns only a bounded window, never the whole blob."
}
func (t *contextSearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"pattern":{"description":"fixed string, or list of fixed strings (match any)","oneOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]},` +
		`"max_results":{"type":"integer","description":"max snippets to return (default 12, capped)"},` +
		`"context_lines":{"type":"integer","description":"lines of context around each hit (default 2, capped)"},` +
		`"id":{"type":"string","description":"stored tool result id (e.g. 0003-bash-ab12cd34.txt); when set, fetch a bounded window of that stored result instead of searching"},` +
		`"offset":{"type":"integer","description":"rune offset into the stored body for get-by-id (default 0)"},` +
		`"window":{"type":"integer","description":"chars to return for get-by-id (default 4000, clamped)"}` +
		`}}`)
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

	// contextSearchMaxRetrieved is the cumulative result budget for one
	// session. The per-call cap prevents one broad query; this cap prevents a
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

	// Get-by-id window: default slice length and hard ceiling. A huge requested
	// window is clamped to contextSearchMaxWindow, then further bounded by the
	// per-call and cumulative retrieval budgets.
	contextSearchDefaultWindow = 4_000
	contextSearchMaxWindow     = 6_000

	// contextSearchMaxStoredRead mirrors the session store's per-file cap
	// (internal/session toolResultMaxReadBytes): stored bodies never exceed
	// it, so a windowed get-by-id read of the whole stored file is always
	// possible. Files above it (legacy) are treated as missing.
	contextSearchMaxStoredRead = 8 << 20
	// contextSearchMaxBudgetSessions bounds the per-session retrieval budget
	// map so long-lived hosts do not leak entries for closed sessions.
	contextSearchMaxBudgetSessions = 128
)

type contextSearchArgs struct {
	Pattern      json.RawMessage `json:"pattern"`
	MaxResults   *int            `json:"max_results"`
	ContextLines *int            `json:"context_lines"`
	ID           string          `json:"id"`
	Offset       *int            `json:"offset"`
	Window       *int            `json:"window"`
}

func (a contextSearchArgs) patterns() ([]string, error) {
	if len(a.Pattern) == 0 {
		return nil, fmt.Errorf("context_search: pattern required")
	}
	validate := func(p string) error {
		if utf8.RuneCountInString(p) > contextSearchMaxPatternRunes {
			return fmt.Errorf("context_search: pattern exceeds %d characters", contextSearchMaxPatternRunes)
		}
		return nil
	}
	var one string
	if err := json.Unmarshal(a.Pattern, &one); err == nil {
		if strings.TrimSpace(one) == "" {
			return nil, fmt.Errorf("context_search: pattern required")
		}
		if err := validate(one); err != nil {
			return nil, err
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(a.Pattern, &many); err != nil {
		return nil, fmt.Errorf("context_search: pattern must be a string or list of strings")
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range many {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if len(out) >= contextSearchMaxPatterns {
			return nil, fmt.Errorf("context_search: at most %d patterns", contextSearchMaxPatterns)
		}
		if err := validate(p); err != nil {
			return nil, err
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("context_search: pattern required")
	}
	return out, nil
}

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

func (t *contextSearchTool) Exec(ctx context.Context, args json.RawMessage) (out string, err error) {
	var a contextSearchArgs
	defer func() {
		if err != nil || out == "" {
			return
		}
		eng := mow.EngineFromContext(ctx)
		if eng == nil {
			return
		}
		mode := "pattern"
		if strings.TrimSpace(a.ID) != "" {
			mode = "id"
		}
		eng.Emit(mow.Event{
			Type:           mow.EventContextSinkRecover,
			Tool:           "context_search",
			StoredID:       strings.TrimSpace(a.ID),
			RecoveredBytes: len(out),
			RecoveryMode:   mode,
		})
	}()
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// Get-by-id path: fetch one stored tool result (the id the context sink
	// stub printed) instead of pattern-searching the archive.
	if a.ID != "" {
		return t.getByID(ctx, a.ID, a.Offset, a.Window)
	}
	patterns, err := a.patterns()
	if err != nil {
		return "", err
	}
	maxResults := clampInt(a.MaxResults, contextSearchDefaultResults, 1, contextSearchMaxResults)
	ctxLines := clampInt(a.ContextLines, contextSearchDefaultCtxLine, 0, contextSearchMaxCtxLines)

	base, sid, key, err := t.searchRoot(ctx)
	if err != nil {
		return "", err
	}

	// Serialize searches so the cumulative budget is an actual session-wide
	// limit even when the agent runs read-only tools in parallel.
	t.mu.Lock()
	defer t.mu.Unlock()

	files := t.archiveFiles(base, sid)
	if len(files) == 0 {
		msg := "(no context archives yet — nothing has been compacted out of context)"
		t.charge(key, len(msg))
		return msg, nil
	}

	// Account the result before returning it. If the session budget is nearly
	// spent, return a small explicit message rather than silently consuming an
	// unbounded amount of context over repeated calls.
	remaining := contextSearchMaxRetrieved - t.retrieved[key]
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
		if !searchArchiveFile(base, rel, patterns, ctxLines, b) {
			break
		}
	}
	if b.results == 0 {
		// Even a miss has a bounded response. Charge a small diagnostic budget so
		// repeated miss queries cannot spin indefinitely for free.
		t.charge(key, contextSearchTruncationReserve)
		return "(no matches in context archive)", nil
	}
	out = strings.TrimRight(b.out.String(), "\n")
	if b.truncated {
		reason := b.stopReason
		if reason == "" {
			reason = "budget"
		}
		out += fmt.Sprintf("\n\n…(stopped at %d snippets / %s; refine the pattern for more)", b.results, reason)
	}
	// Charge the actual bounded payload, including the small truncation note.
	t.charge(key, len(out))
	return out, nil
}

// getByID fetches one stored tool result by its stable id (the id the context
// sink stub printed). The body is returned as a bounded rune window, never the
// whole blob, and the window counts against the same cumulative retrieval
// budget as pattern searches.
func (t *contextSearchTool) getByID(ctx context.Context, id string, offset, window *int) (string, error) {
	if !contextSearchToolIDPattern.MatchString(id) {
		return "", fmt.Errorf("context_search: invalid stored result id %q", id)
	}
	off := clampInt(offset, 0, 0, 1<<30)
	win := clampInt(window, contextSearchDefaultWindow, 0, contextSearchMaxWindow)

	base, sid, key, err := t.searchRoot(ctx)
	if err != nil {
		return "", err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	remaining := contextSearchMaxRetrieved - t.retrieved[key]
	if remaining <= 0 {
		return "(context_search retrieval budget exhausted for this session; refine the task or continue in a new session)", nil
	}
	callCap := min(contextSearchMaxOutput, max(0, remaining-contextSearchTruncationReserve))
	if callCap < 1 {
		return "(context_search retrieval budget exhausted for this session; refine the task or continue in a new session)", nil
	}

	// Stored tool results live in the current session's <sid>.tools dir,
	// beside <sid>.jsonl — never another session's.
	path := filepath.Join(base, sid+".tools", id)
	data, ok := readStoredResult(path)
	if !ok {
		return "", fmt.Errorf("context_search: stored result %s expired or missing", id)
	}
	runes := []rune(data)
	start := min(off, len(runes))
	end := min(start+win, len(runes))
	body := string(runes[start:end])
	out := fmt.Sprintf("[stored id=%s offset=%d chars=%d/%d]\n%s", id, start, len(body), len(runes), body)
	if len(out) > callCap {
		out = clampBytes(out, callCap) + "\n…(window truncated)\n"
	}
	t.charge(key, len(out))
	return out, nil
}

// searchRoot resolves the session to search. Tests pin t.dir/t.sid;
// otherwise the engine is taken from ctx (EngineFromContext) and its session
// used. key is the per-session budget key (dir+"/"+sid).
func (t *contextSearchTool) searchRoot(ctx context.Context) (base, sid, key string, err error) {
	if t.dir != "" {
		return t.dir, t.sid, t.dir + "/" + t.sid, nil
	}
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "", "", "", fmt.Errorf("context_search: no engine in context")
	}
	dir := eng.SessionDir()
	sid = eng.SessionID()
	if dir == "" || sid == "" {
		return "", "", "", fmt.Errorf("context_search: no session — nothing to search")
	}
	return dir, sid, dir + "/" + sid, nil
}

// charge adds n to the session's cumulative retrieval budget. The budget map
// is bounded: long-lived hosts open many sessions, and a closed session's
// entry is only a few bytes — evict an arbitrary entry past the cap.
func (t *contextSearchTool) charge(key string, n int) {
	if t.retrieved == nil {
		t.retrieved = map[string]int{}
	}
	t.retrieved[key] += n
	if len(t.retrieved) > contextSearchMaxBudgetSessions {
		for k := range t.retrieved {
			delete(t.retrieved, k)
			break
		}
	}
}

// archiveFiles lists the current session's <sid>.archive/*.md and
// <sid>.tools/*.txt newest-first (bounded), as paths relative to the session
// base dir. Only this session's dirs are scanned — never sibling sessions'.
func (t *contextSearchTool) archiveFiles(base, sid string) []string {
	var files []string
	scan := func(dir, suffix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
				continue
			}
			files = append(files, filepath.Join(filepath.Base(dir), e.Name()))
		}
	}
	scan(filepath.Join(base, sid+".archive"), ".md")
	scan(filepath.Join(base, sid+".tools"), ".txt")
	// Newest first: sequence prefix sorts chronologically within a session.
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
		// Stored tool results get a "stored " header prefix so the model can
		// tell them apart from compaction archives (matches the tool doc).
		display := rel
		if strings.Contains(rel, ".tools") {
			display = "stored " + filepath.Base(rel)
		}
		if !b.add(renderSnippet(display, lines, start, end, i)) {
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

// readStoredResult reads one stored tool body for get-by-id. Stored bodies
// are capped by the store (contextSearchMaxStoredRead), so the whole file
// always fits in memory; the returned body is then sliced to the requested
// bounded window by the caller. Files above the cap (legacy) read as missing.
func readStoredResult(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	if info.Size() > contextSearchMaxStoredRead {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(f, contextSearchMaxStoredRead+1))
	if err != nil || len(data) == 0 || len(data) > contextSearchMaxStoredRead {
		return "", false
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

func clampBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// snippetKey hashes a snippet's content (minus headers/gutters) for
// cross-file dedup.
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
