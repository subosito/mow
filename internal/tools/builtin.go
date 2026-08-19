// Package tools implements built-in mow tools.
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/policy"
)

// Registry builds the enabled tool set for a policy + enable list.
func Registry(p *policy.Policy, enable []string) []agent.Tool {
	want := map[string]bool{}
	for _, e := range enable {
		want[strings.ToLower(strings.TrimSpace(e))] = true
	}
	var out []agent.Tool
	if want["read"] {
		out = append(out, &readTool{p: p})
	}
	if want["glob"] {
		out = append(out, &globTool{p: p})
	}
	if want["grep"] {
		out = append(out, &grepTool{p: p})
	}
	if want["write"] {
		out = append(out, &writeTool{p: p})
	}
	if want["edit"] {
		out = append(out, &editTool{p: p})
	}
	if want["bash"] {
		out = append(out, &bashTool{p: p})
	}
	return out
}

// pathJailHint describes where file tools may touch (workspace ± extra roots).
func pathJailHint(p *policy.Policy) string {
	if p != nil && len(p.ExtraRoots) > 0 {
		return "under the path jail (workspace or extra roots)"
	}
	return "under the workspace path jail"
}

// emptyResultHint explains a zero-result lookup when extra roots are
// configured. A bare "(no matches)" is correct but useless in a multi-repo
// setup: a relative pattern only ever searches the workspace, so the model
// retries blind. Naming the roots turns two wasted turns into one.
func emptyResultHint(p *policy.Policy, pattern string) string {
	if p == nil || len(p.ExtraRoots) == 0 {
		return ""
	}
	// Absolute patterns already targeted a root; the miss is real.
	if filepath.IsAbs(pattern) {
		return ""
	}
	return fmt.Sprintf(
		"\n(searched %s only. Extra roots are configured: %s — "+
			"relative patterns do not reach them; retry with an absolute path under a root.)",
		p.Workspace, strings.Join(p.ExtraRoots, ", "))
}

type readTool struct{ p *policy.Policy }

func (t *readTool) Name() string { return "read" }
func (t *readTool) Description() string {
	jail := pathJailHint(t.p)
	paging := " Args: path, optional offset (1-based first line) and limit (max lines) to page through a large file."
	if t.p != nil && t.p.Hashline {
		return "Read a UTF-8 text file " + jail + ". Lines are numbered with short hashes (N:hash|text) for hashline edit." + paging +
			" Prefer this over bash cat/head/tail/sed for reading source."
	}
	return "Read a UTF-8 text file " + jail + " (relative → workspace; absolute → must be in jail)." + paging +
		" Prefer this over bash cat/head/tail/sed for reading source."
}
func (t *readTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line to return (default 1)"},"limit":{"type":"integer","description":"max lines to return; omit for the whole file"}},"required":["path"]}`)
}

// defaultReadLines caps an unpaged read. A model asking for a 6000-line file
// almost always wants a region, and shipping the whole thing costs the same
// tokens on every later turn. The notice below tells it how to continue, so
// the cap is a paging hint rather than a dead end.
const defaultReadLines = 2000

func (t *readTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	f, path, err := openJailed(t.p, a.Path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			// Models guess conventional filenames; naming the real neighbors
			// turns a wasted turn into an immediate correction.
			return "", fmt.Errorf("read %s: no such file%s", a.Path, nearbyHint(path))
		}
		return "", err
	}
	defer f.Close()
	lim := t.p.MaxReadBytes
	if lim <= 0 {
		lim = 2 << 20
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(lim+1)))
	if err != nil {
		return "", err
	}
	byteCut := false
	if len(data) > lim {
		data = data[:lim]
		byteCut = true
	}
	hashline := t.p != nil && t.p.Hashline
	return renderRead(string(data), a.Offset, a.Limit, hashline, byteCut, lim), nil
}

// renderRead slices content to the requested line window and appends a notice
// that names the exact next call when there is more to read.
//
// Line numbers stay absolute (file lines, not window-relative) so a hashline
// edit made from a paged read still addresses the right line.
func renderRead(content string, offset, limit int, hashline, byteCut bool, byteLim int) string {
	lines := strings.Split(content, "\n")
	// A trailing newline yields a final empty element that is not a line;
	// remember it so a whole-file read returns the file's exact bytes.
	trailingNL := false
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
		trailingNL = true
	}
	total := len(lines)

	start := offset - 1
	if offset <= 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	n := limit
	if n <= 0 {
		n = defaultReadLines
	}
	end := start + n
	if end > total {
		end = total
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte('\n')
		}
		if hashline {
			fmt.Fprintf(&b, "%6d:%s|%s", i+1, lineHash(lines[i]), lines[i])
		} else {
			b.WriteString(lines[i])
		}
	}
	// Preserve the file's own trailing newline on a whole-file read so byte
	// -exact round-trips (and existing callers) are unaffected by paging.
	if trailingNL && end == total && end > start {
		b.WriteByte('\n')
	}

	switch {
	case end < total:
		fmt.Fprintf(&b, "\n…(showing lines %d-%d of %d; continue with offset=%d)", start+1, end, total, end+1)
	case byteCut:
		// The byte cap hit before the line window ran out: there are more
		// lines on disk than we read, so no honest offset can be named.
		fmt.Fprintf(&b, "\n…(truncated at the %d-byte read cap; use bash with sed/tail to inspect the rest)", byteLim)
	case start > 0:
		fmt.Fprintf(&b, "\n…(showing lines %d-%d of %d; end of file)", start+1, end, total)
	}
	return b.String()
}

type globTool struct{ p *policy.Policy }

// globMaxMatches bounds one glob result.
const globMaxMatches = 500

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "List files matching a glob " + pathJailHint(t.p) + ". Supports ** (recursive, matches zero or more directories). Args: pattern (e.g. **/*.go; absolute under jail OK). Prefer this over bash find/ls: results are bounded and jail-checked."
}
func (t *globTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}`)
}
func (t *globTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	pat := a.Pattern
	if pat == "" {
		pat = "*"
	}
	// Confine to workspace: join pattern with root if relative
	root := t.p.Workspace
	full := pat
	if !filepath.IsAbs(pat) {
		full = filepath.Join(root, pat)
	}
	var matches []string
	var err error
	if hasDoublestar(full) {
		// filepath.Glob has no recursive wildcard: it treats ** as a single
		// path segment, so "**/x.go" matches only one level down and never a
		// file at the root. The tool advertises **/*.go, so implement it.
		matches, err = globRecursive(root, full)
	} else {
		matches, err = filepath.Glob(full)
	}
	if err != nil {
		return "", err
	}
	var lines []string
	for _, m := range matches {
		// enforce jail
		if _, err := t.p.ResolvePath(m); err != nil {
			// try as relative to root
			rel, rerr := filepath.Rel(root, m)
			if rerr != nil {
				continue
			}
			if _, err := t.p.ResolvePath(rel); err != nil {
				continue
			}
			lines = append(lines, rel)
			continue
		}
		rel, err := filepath.Rel(root, m)
		if err != nil {
			lines = append(lines, m)
		} else {
			lines = append(lines, rel)
		}
		if len(lines) >= globMaxMatches {
			lines = append(lines, fmt.Sprintf("…(%d-match limit reached; use a narrower pattern to see the rest)", globMaxMatches))
			break
		}
	}
	if len(lines) == 0 {
		return "(no matches)" + emptyResultHint(t.p, a.Pattern), nil
	}
	return strings.Join(lines, "\n"), nil
}

type grepTool struct{ p *policy.Policy }

const (
	// grepMaxMatches bounds how many hits we collect while walking.
	grepMaxMatches = 48
	// grepMaxFiles bounds how many files appear in one result.
	grepMaxFiles = 8
	// grepMaxPerFile bounds hits printed under one file.
	grepMaxPerFile = 3
	// grepMaxLineChars bounds ONE hit. Without it a single match inside a
	// minified bundle, a base64 blob, or a one-line JSON fixture can consume
	// most of the tool-result budget by itself — the match is what matters,
	// not the 40 KB of noise around it.
	grepMaxLineChars = 240
)

type grepHit struct {
	Path string
	Line int
	Text string
}

// formatGrepHits groups matches by file so a broad pattern is a file index,
// not a 100-line dump. Per-file and per-result caps keep the wall of text down.
func formatGrepHits(hits []grepHit, truncatedWalk bool) string {
	if len(hits) == 0 {
		return "(no matches)"
	}
	type fileHits struct {
		path string
		hits []grepHit
	}
	files := make([]fileHits, 0)
	idx := make(map[string]int, 16)
	for _, h := range hits {
		if i, ok := idx[h.Path]; ok {
			files[i].hits = append(files[i].hits, h)
			continue
		}
		idx[h.Path] = len(files)
		files = append(files, fileHits{path: h.Path, hits: []grepHit{h}})
	}
	var b strings.Builder
	shownFiles := 0
	for _, f := range files {
		if shownFiles >= grepMaxFiles {
			break
		}
		shownFiles++
		fmt.Fprintf(&b, "%s (%d)\n", f.path, len(f.hits))
		show := len(f.hits)
		if show > grepMaxPerFile {
			show = grepMaxPerFile
		}
		for i := 0; i < show; i++ {
			fmt.Fprintf(&b, "  %d:%s\n", f.hits[i].Line, clampGrepLine(f.hits[i].Text))
		}
		if extra := len(f.hits) - show; extra > 0 {
			fmt.Fprintf(&b, "  …(+%d more in this file)\n", extra)
		}
	}
	hiddenFiles := len(files) - shownFiles
	if hiddenFiles > 0 || truncatedWalk || len(hits) >= grepMaxMatches {
		note := "narrow the pattern or pass a path"
		if truncatedWalk {
			note = "walk stopped at match cap; " + note
		}
		fmt.Fprintf(&b, "…(%d files shown, %d files with hits, %d matches; %s)\n",
			shownFiles, len(files), len(hits), note)
	}
	return strings.TrimRight(b.String(), "\n")
}

// clampGrepLine keeps a matched line readable without letting it dominate the
// result. Cuts on a rune boundary so the output stays valid UTF-8.
func clampGrepLine(s string) string {
	if len(s) <= grepMaxLineChars {
		return s
	}
	cut := grepMaxLineChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(line clipped)"
}

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search for a fixed string in files " + pathJailHint(t.p) +
		". Args: pattern, path (optional dir/file, default .; absolute under jail OK). " +
		"Results are grouped by file and capped; do not reprint the dump — cite paths and edit. " +
		"Prefer this over bash grep/rg: hits are bounded, grouped, and jail-checked."
}
func (t *grepTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`)
}
func (t *grepTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern required")
	}
	rel := a.Path
	if rel == "" {
		rel = "."
	}
	root, err := t.p.ResolvePath(rel)
	if err != nil {
		return "", err
	}
	var hits []grepHit
	truncated := false
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Size() > int64(t.p.MaxReadBytes) && t.p.MaxReadBytes > 0 {
			return nil
		}
		// Open under the jail with post-open fd verification so a symlink
		// swap between Walk's lstat and read cannot leak outside bytes.
		f, _, oerr := openJailed(t.p, path, os.O_RDONLY, 0)
		if oerr != nil {
			return nil
		}
		lim := t.p.MaxReadBytes
		if lim <= 0 {
			lim = 2 << 20
		}
		data, rerr := io.ReadAll(io.LimitReader(f, int64(lim+1)))
		_ = f.Close()
		if rerr != nil {
			return nil
		}
		if len(data) > lim {
			data = data[:lim]
		}
		// skip binary-ish
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		relp, _ := filepath.Rel(t.p.Workspace, path)
		for i, line := range lines {
			if strings.Contains(line, a.Pattern) {
				hits = append(hits, grepHit{Path: relp, Line: i + 1, Text: line})
				if len(hits) >= grepMaxMatches {
					truncated = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if len(hits) == 0 {
		return formatGrepHits(hits, truncated) + emptyResultHint(t.p, a.Path), nil
	}
	return formatGrepHits(hits, truncated), nil
}

type writeTool struct{ p *policy.Policy }

func (t *writeTool) Name() string { return "write" }
func (t *writeTool) Description() string {
	return "Write content to a file " + pathJailHint(t.p) + " (creates parent dirs). Returns path + unified diff of the change. Args: path, content."
}
func (t *writeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}
func (t *writeTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if err := t.p.AllowTool("write"); err != nil {
		return "", err
	}
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// Probe prior content via jailed open (missing file → create).
	var old []byte
	created := false
	if _, b, err := readFileJailed(t.p, a.Path); err == nil {
		old = b
	} else if os.IsNotExist(err) {
		created = true
	} else {
		// Jail denial or I/O error — do not write.
		return "", err
	}
	path, err := writeFileJailed(t.p, a.Path, []byte(a.Content), 0o644)
	if err != nil {
		return "", err
	}
	// Display the path relative to the WORKSPACE (not the raw input): a file
	// inside the workspace shows "internal/tools/x.go", an extra-root file
	// shows "../mowi/…" — never a wrong "../mow" when cwd is mow.
	rel := workspaceRel(t.p.Workspace, path)
	if created {
		return formatCreateDiff(rel, a.Content), nil
	}
	return formatReplaceDiff(rel, string(old), a.Content), nil
}

type editTool struct{ p *policy.Policy }

func (t *editTool) Name() string { return "edit" }
func (t *editTool) Description() string {
	jail := pathJailHint(t.p)
	if t.p != nil && t.p.Hashline {
		return "Edit a file " + jail + ". Prefer hashline: path + line_hash (8 hex from read) + new_string replaces that line. Or classic old_string/new_string. Returns path + diff."
	}
	return "Replace old_string with new_string in a file " + jail + " (first occurrence). Returns path + unified diff of the hunk. Args: path, old_string, new_string."
}
func (t *editTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"line_hash":{"type":"string"}},"required":["path","new_string"]}`)
}
func (t *editTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if err := t.p.AllowTool("edit"); err != nil {
		return "", err
	}
	var a struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		LineHash  string `json:"line_hash"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	path, data, err := readFileJailed(t.p, a.Path)
	if err != nil {
		return "", err
	}
	// Workspace-relative display (same rationale as write): in-workspace files
	// never print "../mow", extra-root files print "../mowi/…".
	rel := workspaceRel(t.p.Workspace, path)
	s := string(data)
	var oldSnippet string
	if h := strings.TrimSpace(a.LineHash); h != "" {
		// Hashline replace one line.
		oldLines := strings.Split(s, "\n")
		hh := strings.ToLower(h)
		if len(hh) > 8 {
			hh = hh[:8]
		}
		found := false
		for _, line := range oldLines {
			if lineHash(line) == hh {
				oldSnippet = line
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("edit: line_hash %s not found — re-read the file for a fresh N:hash|line", hh)
		}
		s2, err := applyHashlineEdit(s, hh, a.NewString)
		if err != nil {
			return "", err
		}
		s = s2
	} else {
		if a.OldString == "" {
			return "", fmt.Errorf("old_string or line_hash required")
		}
		if !strings.Contains(s, a.OldString) {
			return "", fmt.Errorf("edit: old_string not found — re-read the file; content may have changed")
		}
		oldSnippet = a.OldString
		s = strings.Replace(s, a.OldString, a.NewString, 1)
	}
	if _, err := writeFileJailed(t.p, a.Path, []byte(s), 0o644); err != nil {
		return "", err
	}
	return formatEditDiff(rel, oldSnippet, a.NewString), nil
}

const (
	maxBashOutputBytes = 100_000
	// bashHeadBytes is the share of the cap kept from the *start* of the
	// output; the rest is a rolling window over the most recent bytes.
	//
	// Shell output puts the answer at the end: the failing assertion, the
	// stack trace, the test summary, the exit diagnostic. Head-only capping
	// discards exactly the part that mattered, so the model re-runs the
	// command with `| tail` and we pay for the same work twice. Keep a small
	// head for the command's opening context and spend the rest on the tail.
	bashHeadBytes = maxBashOutputBytes / 4
	bashTailBytes = maxBashOutputBytes - bashHeadBytes
)

// cappedBuffer keeps command output bounded while the process is running,
// retaining a head prefix and a rolling tail and eliding the middle.
// Returning len(p) after dropping bytes preserves io.Writer semantics so
// os/exec does not turn an output cap into a broken-pipe command failure.
//
// The tail is a fixed ring: a program that writes one byte at a time must not
// cost O(n·bashTailBytes) in memmoves.
type cappedBuffer struct {
	head  bytes.Buffer
	ring  []byte
	pos   int  // next write index into ring
	wrap  bool // ring has wrapped at least once
	total int  // bytes written, including elided ones
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += n
	if b.head.Len() < bashHeadBytes {
		room := bashHeadBytes - b.head.Len()
		if room > len(p) {
			room = len(p)
		}
		_, _ = b.head.Write(p[:room])
		p = p[room:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if b.ring == nil {
		b.ring = make([]byte, bashTailBytes)
	}
	if len(p) >= bashTailBytes {
		copy(b.ring, p[len(p)-bashTailBytes:])
		b.pos = 0
		b.wrap = true
		return n, nil
	}
	c := copy(b.ring[b.pos:], p)
	if c < len(p) {
		copy(b.ring, p[c:])
		b.pos = len(p) - c
		b.wrap = true
	} else {
		b.pos += c
		if b.pos == bashTailBytes {
			b.pos = 0
			b.wrap = true
		}
	}
	return n, nil
}

// tail returns the retained trailing bytes in write order.
func (b *cappedBuffer) tail() []byte {
	if b.ring == nil {
		return nil
	}
	if !b.wrap {
		return b.ring[:b.pos]
	}
	out := make([]byte, 0, bashTailBytes)
	out = append(out, b.ring[b.pos:]...)
	return append(out, b.ring[:b.pos]...)
}

func (b *cappedBuffer) String() string {
	head := b.head.String()
	tail := b.tail()
	if !b.Truncated() {
		return head + string(tail)
	}
	// Tell the model what it lost and how to get it: an unqualified
	// "truncated" makes it guess, usually by re-running the command.
	return fmt.Sprintf(
		"%s\n…(%d bytes elided from the middle; head and tail kept — narrow the command or pipe through grep/tail/sed to see the rest)…\n%s",
		head, b.total-maxBashOutputBytes, tail)
}

func (b *cappedBuffer) Truncated() bool { return b.total > maxBashOutputBytes }

// bashSkipListingClamp leaves test/build pipelines alone (go test | rg FAIL
// must keep the tail). Listing-only rg/ls/find is clamped separately.
func bashSkipListingClamp(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, p := range []string{
		"go test", "go build", "go vet", "go generate",
		"cargo test", "cargo build", "npm test", "npm run", "pnpm ", "yarn ",
		"pytest", "python -m pytest",
		"just ", "make ", "task ",
	} {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

func bashSegments(cmd string) []string {
	raw := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n").Replace(cmd)
	var segs []string
	for _, s := range strings.Split(raw, "\n") {
		if t := strings.TrimSpace(s); t != "" {
			segs = append(segs, t)
		}
	}
	if len(segs) == 0 {
		return []string{cmd}
	}
	return segs
}

func pythonLooksLikeListing(low string) bool {
	for _, p := range []string{"os.walk", "os.listdir", "os.scandir", "pathlib", "glob.glob", "rglob"} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

func segmentIsListingOrSearch(seg string) bool {
	fields := strings.Fields(seg)
	i := 0
	for i < len(fields) && strings.Contains(fields[i], "=") && !strings.HasPrefix(fields[i], "-") {
		i++
	}
	if i >= len(fields) {
		return false
	}
	switch strings.ToLower(filepath.Base(fields[i])) {
	case "rg", "grep", "egrep", "fgrep", "find", "ls", "tree", "awk":
		return true
	case "python", "python3":
		return pythonLooksLikeListing(strings.ToLower(seg))
	default:
		return false
	}
}

// isListingOrSearchBash reports repo-survey commands whose stdout should be
// grep-sized (hit list), not a 100 KiB dump. Test/build pipelines are excluded.
func isListingOrSearchBash(cmd string) bool {
	if bashSkipListingClamp(cmd) {
		return false
	}
	// Check the whole command first: `python3 -c "…; os.walk(…)"` contains
	// semicolons that are not shell separators.
	if pythonLooksLikeListing(strings.ToLower(cmd)) &&
		(strings.Contains(strings.ToLower(cmd), "python3") || strings.Contains(strings.ToLower(cmd), "python")) {
		return true
	}
	for _, seg := range bashSegments(cmd) {
		if segmentIsListingOrSearch(seg) {
			return true
		}
	}
	return false
}

// clampListingOutput applies the grep tool's line/match caps so bash rg/ls/find
// cannot dump the tree into history. First hits stay; the rest is a notice.
func clampListingOutput(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var b strings.Builder
	for i, line := range lines {
		if i >= grepMaxMatches {
			fmt.Fprintf(&b, "…(%d-line listing cap; use the grep tool or narrow the command — do not re-run to dump the rest)\n", grepMaxMatches)
			return b.String()
		}
		b.WriteString(clampGrepLine(line))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

type bashTool struct{ p *policy.Policy }

func (t *bashTool) Name() string    { return "bash" }
func (t *bashTool) Untrusted() bool { return true }
func (t *bashTool) Description() string {
	return "Run a shell command with cwd=workspace (default timeout 300s). Args: command, " +
		"optional timeout_sec for slow builds/test suites. For repo search prefer the grep tool " +
		"(bounded hits). bash rg/find/ls/awk listings are capped — do not reprint them; cite " +
		"paths and edit. Do NOT start long-lived servers in the foreground, and do NOT nest " +
		"another full `mow run/goal` inside bash (it will block the tool until timeout). " +
		"For a smoke server: use proc_start (not bash &); then proc_status / curl / proc_stop."
}
func (t *bashTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_sec":{"type":"integer","description":"override the default timeout for one slow command (e.g. a full test suite)"}},"required":["command"]}`)
}
func (t *bashTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if err := t.p.AllowTool("bash"); err != nil {
		return "", err
	}
	var a struct {
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command required")
	}
	timeout := 300 * time.Second
	if t.p != nil && t.p.BashTimeoutSec > 0 {
		timeout = time.Duration(t.p.BashTimeoutSec) * time.Second
	}
	// A caller may ask for longer for one slow command (test suite, cold
	// build), bounded so a hung command cannot park the loop indefinitely.
	if a.TimeoutSec > 0 {
		maxSec := 900
		if t.p != nil && t.p.MaxBashTimeoutSec > 0 {
			maxSec = t.p.MaxBashTimeoutSec
		}
		req := a.TimeoutSec
		if req > maxSec {
			req = maxSec
		}
		if d := time.Duration(req) * time.Second; d > timeout {
			timeout = d
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()

	// Own process group so timeout can kill the whole tree (bash + children).
	// Do not use CommandContext's auto-kill alone — Wait after kill must not block forever.
	cmd := exec.Command("bash", "-lc", a.Command)
	cmd.Dir = t.p.Workspace
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf cappedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return "", err
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var err error
	select {
	case err = <-done:
		// finished within budget
	case <-cctx.Done():
		// Kill entire process group (negative pgid). Best-effort; then reap with a bound.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			// Wait stuck (grandchild holding pipes, zombie race) — abandon Wait.
			err = context.DeadlineExceeded
		}
	}

	out := buf.String()
	if isListingOrSearchBash(a.Command) {
		out = clampListingOutput(out)
	}
	elapsed := time.Since(started)
	if err != nil {
		if cctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			// Output captured before the kill is kept: a timeout that returns
			// nothing is a dead end and the model just retries it bigger.
			head := "\n"
			if strings.TrimSpace(out) == "" {
				head = "\n(no output was captured before the timeout)\n"
			} else {
				head = "\n(output above is partial — captured before the timeout)\n"
			}
			msg := fmt.Sprintf(
				"%serror: bash timed out after %s.\n"+
					"Next step, pick one:\n"+
					"  - long-lived process (server, watcher, mock)? use proc_start "+
					"(then proc_status / proc_stop). Do NOT use `cmd &` — the bash tool "+
					"kills its process group when it returns.\n"+
					"  - genuinely slow build/test? retry with a larger timeout_sec, or raise "+
					"policy.bash_timeout_sec.\n"+
					"  - do not nest `mow run/goal` inside bash; it blocks until timeout.",
				head, timeout)
			return out + msg, nil
		}
		return out + "\nerror: " + err.Error(), nil
	}
	// Report elapsed for slow commands so the next call can budget a
	// timeout_sec instead of discovering the ceiling by hitting it.
	if elapsed >= 10*time.Second {
		out += fmt.Sprintf("\n(took %s)", elapsed.Round(time.Second))
	}
	if strings.TrimSpace(out) == "" {
		return "(exit 0, no output)", nil
	}
	return out, nil
}

func lineHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func formatHashline(content string) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d:%s|%s\n", i+1, lineHash(line), line)
	}
	return b.String()
}

func applyHashlineEdit(content, hash, newLine string) (string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) < 8 {
		return "", fmt.Errorf("hashline: hash must be 8 hex chars")
	}
	hash = hash[:8]
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if lineHash(line) == hash {
			lines[i] = newLine
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("edit: line_hash %s not found — re-read the file for a fresh N:hash|line", hash)
}

// nearbyHint suggests real files when a read path does not exist: name-stem
// matches from the same directory, else a short directory listing. Keeps a
// guessing model self-correcting in one turn instead of re-globbing.
func nearbyHint(path string) string {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	base := filepath.Base(path)
	stem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	var near, all []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		all = append(all, name)
		lower := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		if stem != "" && (strings.Contains(lower, stem) || strings.Contains(stem, lower)) {
			near = append(near, name)
		}
	}
	const maxShow = 8
	pick := near
	label := " — nearby: "
	if len(pick) == 0 {
		pick = all
		label = " — directory contains: "
	}
	sort.Strings(pick)
	extra := ""
	if len(pick) > maxShow {
		extra = fmt.Sprintf(" (+%d more)", len(pick)-maxShow)
		pick = pick[:maxShow]
	}
	return label + strings.Join(pick, ", ") + extra
}
