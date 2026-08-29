// Package tools implements built-in mow tools.
package tools

import (
	"bufio"
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
	"github.com/subosito/mow/internal/sandbox"
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
	hint := " Not a tree survey: grep (or a narrow glob) first, then read the file you will change. Prefer this over bash cat/head/tail/sed."
	if t.p != nil && t.p.Hashline {
		return "Read a UTF-8 text file " + jail + ". Lines are numbered with short hashes (N:hash|text) for hashline edit." + paging + hint
	}
	return "Read a UTF-8 text file " + jail + " (relative → workspace; absolute → must be in jail)." + paging + hint
}
func (t *readTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line to return (default 1)"},"limit":{"type":"integer","description":"max lines to return (default 2000)"}},"required":["path"]}`)
}

// defaultReadLines caps an unpaged read. A model asking for a 6000-line file
// almost always wants a region, and shipping the whole thing costs the same
// tokens on every later turn. The notice below tells it how to continue, so
// the cap is a paging hint rather than a dead end.
const defaultReadLines = 2000

// sniffBytes is the prefix inspected for NULs before a read is treated as text.
const sniffBytes = 8192

// maxReadLineBytes is one Scanner token. A minified one-line blob larger than
// this is refused as binary rather than loaded into the session.
const maxReadLineBytes = 1 << 20

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
			return "", fmt.Errorf("read %s: no such file%s", a.Path, nearbyHint(path))
		}
		return "", err
	}
	defer f.Close()
	lim := t.p.MaxReadBytes
	if lim <= 0 {
		lim = 2 << 20
	}
	br := bufio.NewReaderSize(f, 64*1024)
	peek, _ := br.Peek(sniffBytes)
	if isBinaryPrefix(peek) {
		return "", fmt.Errorf("read %s: not a UTF-8 text file (binary)", a.Path)
	}
	hashline := t.p != nil && t.p.Hashline
	return renderReadFrom(br, a.Offset, a.Limit, hashline, lim, a.Path)
}

func isBinaryPrefix(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return true
	}
	if len(b) > 0 && len(b) < sniffBytes && !utf8.Valid(b) {
		return true
	}
	return false
}

// renderRead is the in-memory paging helper used by tests. Exec uses
// renderReadFrom so offset applies before the byte cap.
func renderRead(content string, offset, limit int, hashline, byteCut bool, byteLim int) string {
	lines := strings.Split(content, "\n")
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
	if trailingNL && end == total && end > start {
		b.WriteByte('\n')
	}
	switch {
	case end < total:
		fmt.Fprintf(&b, "\n…(showing lines %d-%d of %d; continue with offset=%d)", start+1, end, total, end+1)
	case byteCut:
		fmt.Fprintf(&b, "\n…(truncated at the %d-byte read cap)", byteLim)
	case start > 0:
		fmt.Fprintf(&b, "\n…(showing lines %d-%d of %d; end of file)", start+1, end, total)
	}
	return b.String()
}

// renderReadFrom pages by skipping offset-1 lines first, then collecting at
// most limit lines (default 2000) or byteLim of returned content.
func renderReadFrom(r io.Reader, offset, limit int, hashline bool, byteLim int, name string) (string, error) {
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = defaultReadLines
	}
	if byteLim <= 0 {
		byteLim = 2 << 20
	}
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64*1024)
	}
	skip := offset - 1
	var lines []string
	n := 0
	bytesOut := 0
	more := false
	byteCut := false
	for {
		line, err := br.ReadString('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		n++
		hadNL := strings.HasSuffix(line, "\n")
		text := strings.TrimSuffix(line, "\n")
		if n <= skip {
			if err != nil && !errors.Is(err, io.EOF) {
				return "", err
			}
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		add := len(text)
		if hadNL {
			add++
		}
		if len(lines) >= limit {
			more = true
			break
		}
		if bytesOut+add > byteLim {
			more = true
			byteCut = true
			if len(lines) == 0 {
				cut := byteLim
				if cut > len(text) {
					cut = len(text)
				}
				for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
					cut--
				}
				if cut == 0 {
					cut = min(byteLim, len(text))
				}
				lines = append(lines, text[:cut])
			}
			break
		}
		if hadNL {
			lines = append(lines, text+"\n")
		} else {
			lines = append(lines, text)
		}
		bytesOut += add
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	start := offset
	var b strings.Builder
	shown := 0
	for i, line := range lines {
		b.WriteString(line)
		shown++
		_ = i
	}
	end := start + shown - 1
	if shown == 0 {
		end = start - 1
	}
	body := b.String()
	if hashline {
		var hb strings.Builder
		raw := body
		parts := strings.SplitAfter(raw, "\n")
		ln := start
		first := true
		for _, part := range parts {
			if part == "" {
				continue
			}
			nl := strings.HasSuffix(part, "\n")
			text := strings.TrimSuffix(part, "\n")
			if !first {
				hb.WriteByte('\n')
			}
			first = false
			fmt.Fprintf(&hb, "%6d:%s|%s", ln, lineHash(text), text)
			if nl {
				hb.WriteByte('\n')
			}
			ln++
		}
		body = hb.String()
	}
	switch {
	case more && !byteCut:
		fmt.Fprintf(&b, "")
		body += fmt.Sprintf("\n…(showing lines %d-%d; continue with offset=%d)", start, end, end+1)
	case byteCut:
		body += fmt.Sprintf("\n…(truncated at the %d-byte read cap; continue with offset=%d)", byteLim, end+1)
	case start > 1 && !more:
		body += fmt.Sprintf("\n…(showing lines %d-%d; end of file)", start, end)
	}
	return body, nil
}

type globTool struct {
	p *policy.Policy
	// noFd forces the WalkDir fallback (tests).
	noFd bool
	// fdBin, when set, is used instead of PATH lookup (tests: missing/broken fd).
	fdBin string
}

// globMaxMatches bounds one glob result.
const globMaxMatches = 500

// globIndexHint: above this many hits, remind the model glob is an index.
const globIndexHint = 40

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "List files matching a glob " + pathJailHint(t.p) +
		". Supports ** (recursive). Args: pattern (e.g. internal/**/*.go; absolute under jail OK). " +
		"Uses fd when installed (same jail and caps); otherwise a bounded walk. " +
		"Results are an index (capped) — do not read every path; grep for a symbol, then read 1–3 files you will edit. " +
		"Prefer a directory-prefixed pattern over **/* . Do not bash find/fd/ls."
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
	root := t.p.Workspace
	full := pat
	if !filepath.IsAbs(pat) {
		full = filepath.Join(root, pat)
	} else if jail, _, ok := t.p.Beneath(filepath.Clean(pat)); ok {
		root = jail
	}
	var matches []string
	var err error
	if hasDoublestar(full) {
		matches, err = t.globRecursive(ctx, root, full)
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
			lines = append(lines, fmt.Sprintf("…(%d-match limit; this is an index — grep or read a few files, do not read every path; narrower pattern to see the rest)", globMaxMatches))
			break
		}
	}
	if len(lines) == 0 {
		return "(no matches)" + emptyResultHint(t.p, a.Pattern), nil
	}
	body := strings.Join(lines, "\n")
	if len(lines) >= globIndexHint && len(lines) < globMaxMatches {
		body += fmt.Sprintf("\n…(%d files; this is an index — grep or read a few files you will edit, do not read every path)", len(lines))
	}
	return body, nil
}

func (t *globTool) globRecursive(ctx context.Context, root, pat string) ([]string, error) {
	if !t.noFd {
		bin := t.fdBin
		if bin == "" {
			bin = lookupFd()
		}
		if bin != "" {
			if out, ok := globFd(ctx, bin, root, pat); ok {
				return out, nil
			}
		}
	}
	return globWalk(root, pat)
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
	a.Content = stripHashlineChrome(a.Content)
	if hasReadPagingBanner(a.Content) {
		return "", fmt.Errorf("write: content looks like a paged read (contains a truncation banner) — re-read the whole file or omit the …(showing lines…) footer")
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
	return "Replace old_string with new_string in a file " + jail + " (must be unique whole lines). Returns path + unified diff of the hunk. Args: path, old_string, new_string."
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
	orig := string(data)
	s := orig
	newStr := stripHashlineChrome(a.NewString)
	var oldSnippet string
	if h := strings.TrimSpace(a.LineHash); h != "" {
		s2, err := applyHashlineEdit(s, h, newStr)
		if err != nil {
			return "", err
		}
		hh := strings.ToLower(strings.TrimSpace(h))
		if len(hh) > 8 {
			hh = hh[:8]
		}
		for _, line := range strings.Split(s, "\n") {
			if lineHash(line) == hh {
				oldSnippet = line
				break
			}
		}
		s = s2
	} else {
		if a.OldString == "" {
			return "", fmt.Errorf("old_string or line_hash required")
		}
		oldSnippet = stripHashlineChrome(a.OldString)
		replaced, err := replaceOnceLineAnchored(s, oldSnippet, newStr)
		if err != nil {
			return "", err
		}
		s = replaced
	}
	if _, err := writeFileJailed(t.p, a.Path, []byte(s), 0o644); err != nil {
		return "", err
	}
	return formatEditDiff(rel, oldSnippet, newStr, firstLineOf(orig, oldSnippet)), nil
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

type bashTool struct{ p *policy.Policy }

func (t *bashTool) Name() string    { return "bash" }
func (t *bashTool) Untrusted() bool { return true }
func (t *bashTool) Description() string {
	jail := "Not path-jailed: the process runs as you and can leave the workspace. "
	if t.p.SandboxEnabled() {
		jail = "Sandboxed (bubblewrap): only the workspace and configured extra " +
			"roots are visible/writable — $HOME and the rest of the filesystem " +
			"are not. Network is still ON. "
	}
	return "Run a shell command with cwd=workspace (default timeout 300s). " +
		"Requires --allow-shell. " + jail + "Args: command, optional timeout_sec for slow " +
		"builds/test suites. Do not rg/grep/find/fd/ls — use grep or glob " +
		"(those bash forms are refused). " +
		"Output is kept as-is (byte-budgeted only) — keep commands scoped. Do NOT start " +
		"long-lived servers in the foreground, and do NOT nest another full " +
		"`mow run/goal` inside bash (it will block the tool until timeout). For a " +
		"smoke server: use proc_start (not bash &); then proc_status / curl / proc_stop."
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
	// Opt-in OS jail (policy.sandbox / --sandbox=bwrap). Identity by default;
	// when enabled it rewrites argv to `bwrap ... -- bash -lc <command>` and
	// keeps the process group above intact so the timeout kill still works.
	// A configured-but-unavailable sandbox is a hard error: never fall back to
	// an unsandboxed shell.
	if be, err := t.p.SandboxBackend(); err != nil {
		return "", err
	} else if wrapped, err := sandbox.WithNewSession(be, true).Wrap(cmd); err != nil {
		return "", err
	} else {
		cmd = wrapped
	}
	var buf cappedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	// Background children (`cmd &`) keep stdout open; without WaitDelay,
	// Wait hangs until they exit. Bound that, then kill the group.
	cmd.WaitDelay = 200 * time.Millisecond

	if err := cmd.Start(); err != nil {
		return "", err
	}
	pid := cmd.Process.Pid
	pgid := pid
	if g, gerr := syscall.Getpgid(pid); gerr == nil {
		pgid = g
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var err error
	select {
	case err = <-done:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		if err != nil && errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
	case <-cctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			err = context.DeadlineExceeded
		}
	}

	out := buf.String()
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

// replaceOnceLineAnchored replaces the first old with new, but only when old
// is a whole-line span (starts after \n or BOF, ends before \n or EOF).
// A mid-line unique snippet used to splice (e.g. YAML `design advice` leftover
// plus duplicated tail) and leave invalid files. Copy whole lines from read.
func replaceOnceLineAnchored(content, old, new string) (string, error) {
	if old == "" {
		return "", fmt.Errorf("old_string or line_hash required")
	}
	idx := strings.Index(content, old)
	if idx < 0 {
		return "", fmt.Errorf("edit: old_string not found — re-read the file; content may have changed")
	}
	if strings.Count(content, old) > 1 {
		return "", fmt.Errorf("edit: old_string matches more than once — include more surrounding lines so the match is unique")
	}
	if idx > 0 && !isLineStart(content, idx) {
		return "", fmt.Errorf("edit: old_string must start at a line boundary — re-read and copy whole lines")
	}
	end := idx + len(old)
	if end < len(content) && !isLineEnd(content, end) && !strings.HasSuffix(old, "\n") {
		return "", fmt.Errorf("edit: old_string must end at a line boundary — re-read and copy whole lines")
	}
	// old ended with a newline but new did not: the following line glued on
	// (YAML `description: bar    models:`). Keep the break.
	if strings.HasSuffix(old, "\n") && !strings.HasSuffix(new, "\n") {
		new += "\n"
	}
	skip := overlapRestBytes(new, content[end:])
	return content[:idx] + new + content[end+skip:], nil
}

func isLineStart(content string, idx int) bool {
	if idx <= 0 {
		return true
	}
	c := content[idx-1]
	return c == '\n' || c == '\r'
}

func isLineEnd(content string, end int) bool {
	if end >= len(content) {
		return true
	}
	c := content[end]
	return c == '\n' || c == '\r'
}

func hasReadPagingBanner(s string) bool {
	return strings.Contains(s, "…(showing lines ") ||
		strings.Contains(s, "…(truncated at the ")
}

// overlapRestBytes is how much of rest to drop because new already contains
// that tail (copied following lines). Require a real line, not `}` / `end`.
func overlapRestBytes(new, rest string) int {
	if rest == "" {
		return 0
	}
	nl := 0
	switch {
	case strings.HasPrefix(rest, "\r\n"):
		rest = rest[2:]
		nl = 2
	case strings.HasPrefix(rest, "\n"), strings.HasPrefix(rest, "\r"):
		rest = rest[1:]
		nl = 1
	}
	np := strings.Split(strings.TrimRight(new, "\n"), "\n")
	rp := strings.Split(strings.TrimRight(rest, "\n"), "\n")
	k := overlappingLineCount(np, rp)
	if k == 0 {
		return 0
	}
	consumed := 0
	remain := rest
	for i := 0; i < k; i++ {
		if !strings.HasPrefix(remain, rp[i]) {
			return 0
		}
		consumed += len(rp[i])
		remain = remain[len(rp[i]):]
		if strings.HasPrefix(remain, "\r\n") {
			consumed += 2
			remain = remain[2:]
		} else if strings.HasPrefix(remain, "\n") || strings.HasPrefix(remain, "\r") {
			consumed++
			remain = remain[1:]
		}
	}
	return nl + consumed
}

func overlappingLineCount(newParts, restParts []string) int {
	max := len(newParts)
	if len(restParts) < max {
		max = len(restParts)
	}
	for cand := max; cand >= 1; cand-- {
		ok := true
		for i := 0; i < cand; i++ {
			if newParts[len(newParts)-cand+i] != restParts[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if cand >= 2 || (cand == 1 && len(restParts[0]) >= 8) {
			return cand
		}
	}
	return 0
}

// stripHashlineChrome removes read-tool `N:hash|` prefixes if the model pasted
// a hashline dump back into edit. Leaving them in the file is the "random
// numbers at the start of every line" failure.
func stripHashlineChrome(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	changed := false
	for i, line := range lines {
		if stripped, ok := stripHashlineChromeLine(line); ok {
			lines[i] = stripped
			changed = true
		}
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

func stripHashlineChromeLine(line string) (string, bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	j := i
	if j >= len(line) || line[j] < '0' || line[j] > '9' {
		return line, false
	}
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j >= len(line) || line[j] != ':' {
		return line, false
	}
	j++
	if j+8 >= len(line) {
		return line, false
	}
	for k := 0; k < 8; k++ {
		c := line[j+k]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return line, false
		}
	}
	j += 8
	if j >= len(line) || line[j] != '|' {
		return line, false
	}
	return line[j+1:], true
}

func applyHashlineEdit(content, hash, newLine string) (string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 8 {
		return "", fmt.Errorf("hashline: hash must be 8 hex chars")
	}
	for i := 0; i < 8; i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("hashline: hash must be 8 hex chars")
		}
	}
	lines := strings.Split(content, "\n")
	found := -1
	for i, line := range lines {
		if lineHash(line) != hash {
			continue
		}
		if found >= 0 {
			return "", fmt.Errorf("edit: line_hash %s matches more than one line — use old_string with a unique snippet or re-read", hash)
		}
		found = i
	}
	if found < 0 {
		return "", fmt.Errorf("edit: line_hash %s not found — re-read the file for a fresh N:hash|line", hash)
	}
	newLine = stripHashlineChrome(newLine)
	newLine = strings.TrimRight(newLine, "\n")
	parts := strings.Split(newLine, "\n")
	rest := lines[found+1:]
	if k := overlappingLineCount(parts, rest); k > 0 {
		rest = rest[k:]
	}
	out := append(append(append([]string{}, lines[:found]...), parts...), rest...)
	return strings.Join(out, "\n"), nil
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
