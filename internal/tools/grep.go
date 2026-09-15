package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow/internal/policy"
)

type grepTool struct {
	p *policy.Policy
	// noRipgrep forces the WalkDir fallback (tests).
	noRipgrep bool
	// rgBin, when set, is used instead of PATH lookup (tests: missing/broken rg).
	rgBin string
}

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

// grepOptions keeps compact defaults while allowing bounded expanded results.
type grepOptions struct {
	limit     int
	filesOnly bool
	expanded  bool
}

func grepSettings(opts []grepOptions) grepOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return grepOptions{limit: grepMaxMatches}
}

type grepHit struct {
	Path string
	Line int
	Text string
}

// lookupRipgrep is exec.LookPath("rg"); tests may stub it.
var lookupRipgrep = func() string {
	p, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return p
}

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Repo content search (literal by default; regex:true enables regular expressions) " + pathJailHint(t.p) +
		". Call this instead of bash rg/grep. Args: pattern, regex (optional boolean; use true for alternatives like foo|bar or escaped symbols), path (optional dir/file, default .; absolute under jail OK). " +
		"Uses ripgrep when installed (same jail and caps); otherwise a bounded walk. " +
		"Results default to a compact sample. Set max_results (1–500) to expand matching lines, or files_only:true to list matching filenames (default 100). Truncated results are not exhaustive; narrow path/pattern if needed."
}
func (t *grepTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer","minimum":1,"maximum":500,"description":"Maximum matching lines, or filenames with files_only. Omit for a compact sample."},"files_only":{"type":"boolean","description":"Return matching filenames instead of line excerpts; default false."},"regex":{"type":"boolean","description":"Interpret pattern as a regular expression; default false searches the exact literal text."}},"required":["pattern"]}`)
}

func (t *grepTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Regex      bool   `json:"regex"`
		MaxResults *int   `json:"max_results"`
		FilesOnly  bool   `json:"files_only"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern required")
	}
	opts := grepOptions{limit: grepMaxMatches, filesOnly: a.FilesOnly, expanded: a.MaxResults != nil}
	if a.FilesOnly {
		opts.limit = 100
	}
	if a.MaxResults != nil {
		if *a.MaxResults < 1 || *a.MaxResults > 500 {
			return "", fmt.Errorf("max_results must be between 1 and 500")
		}
		opts.limit = *a.MaxResults
	}
	var re *regexp.Regexp
	if a.Regex {
		var err error
		re, err = regexp.Compile(a.Pattern)
		if err != nil {
			return "", fmt.Errorf("invalid grep regex: %w", err)
		}
	}

	rel := a.Path
	if rel == "" {
		rel = "."
	}
	root, err := t.p.ResolvePath(rel)
	if err != nil {
		return "", err
	}
	hits, truncated, err := t.search(ctx, a.Pattern, root, re, opts)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		out := formatGrepHits(hits, truncated, opts) + emptyResultHint(t.p, a.Path)
		if !a.Regex && strings.ContainsAny(a.Pattern, `|\^$*+?()[]{}`) {
			out += "\nSearch was literal. For a regular expression (including alternatives or escaped symbols), retry with regex:true; otherwise search a simpler literal."
		}
		return out, nil
	}
	return formatGrepHits(hits, truncated, opts), nil
}

func (t *grepTool) ripgrepBin() string {
	if t.noRipgrep {
		return ""
	}
	if t.rgBin != "" {
		return t.rgBin
	}
	return lookupRipgrep()
}

func (t *grepTool) search(ctx context.Context, pattern, root string, re *regexp.Regexp, options ...grepOptions) ([]grepHit, bool, error) {
	opts := grepSettings(options)
	if bin := t.ripgrepBin(); bin != "" {
		hits, truncated, ok := t.grepRipgrep(ctx, bin, pattern, root, re, opts)
		if ok {
			return hits, truncated, nil
		}
	}
	return t.grepWalk(ctx, pattern, root, re, opts)
}

// grepRipgrep runs rg --json (literal or regex) under the already-resolved jail
// root. Hits outside the jail are dropped. false means "caller should walk".
func (t *grepTool) grepRipgrep(ctx context.Context, bin, pattern, root string, re *regexp.Regexp, options ...grepOptions) ([]grepHit, bool, bool) {
	opts := grepSettings(options)
	lim := 2 << 20
	if t.p != nil && t.p.MaxReadBytes > 0 {
		lim = t.p.MaxReadBytes
	}
	args := []string{
		"--json",
		"--no-config",
		"--hidden",
		"--no-ignore",
		"--no-messages",
		"--max-columns", fmt.Sprintf("%d", grepMaxLineChars*2),
		"--max-columns-preview",
		"--max-filesize", fmt.Sprintf("%d", lim),
	}
	if opts.filesOnly {
		args = append(args, "--max-count", "1")
	}
	if re == nil {
		args = append(args, "--fixed-strings")
	}
	for name := range skipDirNames {
		args = append(args, "--glob", "!"+name, "--glob", "!"+name+"/**")
	}
	args = append(args, "--", pattern, ".")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, false, false
	}
	hits, truncated := parseRipgrepJSON(stdout, t.p, root, opts)
	if truncated && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if len(hits) > 0 || ctx.Err() != nil {
		return hits, truncated, true
	}
	if waitErr == nil {
		return hits, truncated, true // genuine miss
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ExitCode() == 1 {
		return hits, truncated, true // rg: no matches
	}
	// Missing flags, crash, not executable, … → walk. Never fail the tool
	// just because rg is absent or too old.
	return nil, false, false
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

func parseRipgrepJSON(r io.Reader, p *policy.Policy, root string, options ...grepOptions) ([]grepHit, bool) {
	opts := grepSettings(options)
	seen := map[string]bool{}
	dec := json.NewDecoder(r)
	var hits []grepHit
	truncated := false
	for {
		var ev rgEvent
		if err := dec.Decode(&ev); err != nil {
			break
		}
		if ev.Type != "match" || ev.Data.Path.Text == "" || ev.Data.LineNumber <= 0 {
			continue
		}
		full := ev.Data.Path.Text
		if !filepath.IsAbs(full) {
			full = filepath.Join(root, full)
		}
		if _, err := p.ResolvePath(full); err != nil {
			continue
		}
		relp, err := filepath.Rel(p.Workspace, full)
		if err != nil {
			relp = full
		}
		if opts.filesOnly && seen[relp] {
			continue
		}
		seen[relp] = true
		line := strings.TrimRight(ev.Data.Lines.Text, "\n\r")
		hits = append(hits, grepHit{Path: relp, Line: ev.Data.LineNumber, Text: line})
		if len(hits) > opts.limit {
			hits = hits[:opts.limit]
			truncated = true
			break
		}
	}
	return hits, truncated
}

// skipGrepExt: the walk never opens these (rg already skips binaries).
var skipGrepExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true,
	".mp3": true, ".mp4": true, ".webm": true, ".mov": true, ".wav": true,
	".pdf": true, ".wasm": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".o": true, ".a": true, ".class": true, ".jar": true, ".pyc": true,
}

// grepWalk is the no-rg path: WalkDir + line scan, same jail and caps as rg.
func (t *grepTool) grepWalk(ctx context.Context, pattern, root string, re *regexp.Regexp, options ...grepOptions) ([]grepHit, bool, error) {
	opts := grepSettings(options)
	lim := 2 << 20
	if t.p != nil && t.p.MaxReadBytes > 0 {
		lim = t.p.MaxReadBytes
	}
	var hits []grepHit
	truncated := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipGrepExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > int64(lim) {
			return nil
		}
		f, _, oerr := openJailed(t.p, path, os.O_RDONLY, 0)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		br := bufio.NewReaderSize(f, 64*1024)
		peek, _ := br.Peek(sniffBytes)
		if isBinaryPrefix(peek) {
			return nil
		}
		sc := bufio.NewScanner(io.LimitReader(br, int64(lim)))
		sc.Buffer(make([]byte, 64*1024), maxReadLineBytes)
		relp, _ := filepath.Rel(t.p.Workspace, path)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if (re == nil && !strings.Contains(line, pattern)) || (re != nil && !re.MatchString(line)) {
				continue
			}
			hits = append(hits, grepHit{Path: relp, Line: lineNo, Text: line})
			if len(hits) > opts.limit {
				hits = hits[:opts.limit]
				truncated = true
				return filepath.SkipAll
			}
			if opts.filesOnly {
				break
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return nil, false, err
	}
	return hits, truncated, nil
}

// formatGrepHits groups matches by file so a broad pattern is a file index,
// not a 100-line dump. Per-file and per-result caps keep the wall of text down.
func formatGrepHits(hits []grepHit, truncatedWalk bool, options ...grepOptions) string {
	opts := grepSettings(options)
	if len(hits) == 0 {
		return "(no matches)"
	}
	if opts.filesOnly {
		paths := make([]string, 0, len(hits))
		for _, h := range hits {
			paths = append(paths, h.Path)
		}
		if truncatedWalk {
			paths = append(paths, "…(more matching files; increase max_results up to 500 or narrow path/pattern)")
		}
		return strings.Join(paths, "\n")
	}
	maxFiles, maxPerFile := grepMaxFiles, grepMaxPerFile
	if opts.expanded {
		maxFiles, maxPerFile = opts.limit, opts.limit
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
		if shownFiles >= maxFiles {
			break
		}
		shownFiles++
		fmt.Fprintf(&b, "%s (%d)\n", f.path, len(f.hits))
		show := len(f.hits)
		if show > maxPerFile {
			show = maxPerFile
		}
		for i := 0; i < show; i++ {
			fmt.Fprintf(&b, "  %d:%s\n", f.hits[i].Line, clampGrepLine(f.hits[i].Text))
		}
		if extra := len(f.hits) - show; extra > 0 {
			fmt.Fprintf(&b, "  …(+%d more in this file)\n", extra)
		}
	}
	hiddenFiles := len(files) - shownFiles
	if hiddenFiles > 0 || truncatedWalk {
		note := "increase max_results up to 500, use files_only:true, or narrow path/pattern"
		if truncatedWalk {
			note = "walk stopped at match cap; " + note
		}
		fmt.Fprintf(&b, "…(%d files shown, %d files with hits, %d matches; %s)\n",
			shownFiles, len(files), len(hits), note)
	}
	return strings.TrimRight(b.String(), "\n")
}

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
