package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/subosito/mow/internal/llm"
)

// Soft exploration helpers — no hard kill on explore streak. Long runs stop on
// model finish, ctx cancel, or MaxTurns. These:
//  1. stub re-reads (read + bash cat/sed/head/tail/grep of same paths)
//  2. stub repeated inventory (git status/log/ls/find)
//  3. soft-block destructive git/rm that discards WIP
//  4. treat test/build/commit bash as productive (resets explore streak)
//  5. warn every turn after exploreWarnEvery consecutive explore turns
const (
	exploreWarnEvery  = 6
	rereadLimit       = 1 // second access to same path stubs
	inventoryLimit    = 2 // third identical inventory class stubs
	sameToolWarnAfter = 3
)

// thrashState tracks per-Prompt exploration for soft hints only.
type thrashState struct {
	mu        sync.Mutex
	workspace string // absolute; used to unify abs vs relative paths
	// path → times accessed this Prompt (read tool + bash file views)
	reads map[string]int
	// inventory class key → times (git-status, ls, find, …)
	inv map[string]int
	// exact tool name+normalized args → times
	calls map[string]int
	// consecutive turns whose tools were all explore-only
	exploreStreak int
}

func newThrashState(workspace string) *thrashState {
	ws, _ := filepath.Abs(strings.TrimSpace(workspace))
	return &thrashState{
		workspace: ws,
		reads:     make(map[string]int),
		inv:       make(map[string]int),
		calls:     make(map[string]int),
	}
}

func isExploreToolCall(tc llm.ToolCall) bool {
	name := strings.ToLower(strings.TrimSpace(tc.Function.Name))
	switch name {
	case "read", "glob", "grep":
		return true
	case "write", "edit":
		return false
	case "bash":
		cmd := toolArgString(json.RawMessage(tc.Function.Arguments), "command")
		return isExploreBash(cmd)
	default:
		// Custom tools (media, acp_delegate, …) count as productive action.
		return false
	}
}

func isExploreBash(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true
	}
	if isProductiveBash(cmd) {
		return false
	}
	return true
}

func isProductiveBash(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, p := range []string{
		"go test", "go build", "go vet", "go generate", "gofmt -w", "go fmt",
		"cargo test", "cargo build", "npm test", "npm run", "pnpm ", "yarn ",
		"pytest", "python -m pytest",
		"just ", "make ", "task ",
		"git add", "git commit", "git push", "git stash", "git mv",
		"git checkout -b", "git switch -c",
	} {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

// isDestructiveBash detects WIP-destroying commands (soft-blocked, not executed).
func isDestructiveBash(cmd string) bool {
	c := strings.ToLower(cmd)
	// Discard uncommitted file changes
	if strings.Contains(c, "git restore ") || strings.Contains(c, "git restore\t") {
		return true
	}
	if strings.Contains(c, "git checkout --") || strings.Contains(c, "git checkout\t--") {
		return true
	}
	if strings.Contains(c, "git reset --hard") || strings.Contains(c, "git reset --merge") {
		return true
	}
	if strings.Contains(c, "git clean -") {
		return true
	}
	// Recursive force remove of project trees (not /tmp alone)
	if reDestructiveRM.MatchString(c) {
		// Allow rm of clearly temp-only paths
		if strings.Contains(c, "/tmp/") || strings.Contains(c, "$tmp") || strings.Contains(c, "${tmp") {
			return false
		}
		return true
	}
	return false
}

var reDestructiveRM = regexp.MustCompile(`\brm\s+(-[a-zA-Z]*r[a-zA-Z]*f|-rf|-fr)\b`)

func batchExploreOnly(calls []llm.ToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, tc := range calls {
		if !isExploreToolCall(tc) {
			return false
		}
	}
	return true
}

// noteTurn updates explore streak. After exploreWarnEvery, warns every turn.
func (s *thrashState) noteTurn(calls []llm.ToolCall) (warn bool) {
	if s == nil {
		return false
	}
	if !batchExploreOnly(calls) {
		s.exploreStreak = 0
		return false
	}
	s.exploreStreak++
	return s.exploreStreak >= exploreWarnEvery
}

func (s *thrashState) maybeDedupeRead(args json.RawMessage) (string, bool) {
	if s == nil {
		return "", false
	}
	path := toolArgString(args, "path")
	if path == "" {
		return "", false
	}
	return s.notePathAccess(path)
}

// maybeDedupeBash short-circuits destructive, repeated inventory, and re-views.
func (s *thrashState) maybeDedupeBash(args json.RawMessage) (string, bool) {
	if s == nil {
		return "", false
	}
	cmd := toolArgString(args, "command")
	if cmd == "" {
		return "", false
	}
	if isDestructiveBash(cmd) {
		return "(blocked by harness: discarding uncommitted work is not allowed " +
			"(git checkout/restore/reset --hard, git clean, rm -rf of project trees). " +
			"Fix compile/test errors in place with edit/write; do not wipe WIP for green.)", true
	}
	if key := inventoryKey(cmd); key != "" {
		s.mu.Lock()
		n := s.inv[key]
		s.inv[key] = n + 1
		s.mu.Unlock()
		if n >= inventoryLimit {
			return fmt.Sprintf(
				"(inventory %q already ran %d times this prompt — stop re-listing the tree. "+
					"Next tools should be edit/write, or finish with a concrete blocker.)",
				key, n+1,
			), true
		}
	}
	paths := bashReadPaths(cmd)
	if len(paths) == 0 {
		return "", false
	}
	allSeen := true
	var keys []string
	for _, p := range paths {
		key := s.pathKey(p)
		if key == "" {
			continue
		}
		keys = append(keys, key)
		s.mu.Lock()
		n := s.reads[key]
		s.reads[key] = n + 1
		s.mu.Unlock()
		if n < rereadLimit {
			allSeen = false
		}
	}
	if len(keys) == 0 || !allSeen {
		return "", false
	}
	shown := keys
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return fmt.Sprintf(
		"(already viewed %s this prompt — content unchanged; do not re-cat/sed/grep the same files. "+
			"Use the earlier result, then act (edit/write) or finish.)",
		strings.Join(shown, ", "),
	), true
}

func (s *thrashState) notePathAccess(path string) (string, bool) {
	key := s.pathKey(path)
	if key == "" {
		return "", false
	}
	s.mu.Lock()
	n := s.reads[key]
	s.reads[key] = n + 1
	s.mu.Unlock()
	if n < rereadLimit {
		return "", false
	}
	return fmt.Sprintf(
		"(already read %q this prompt — content unchanged; do not re-read. "+
			"Use the earlier result, then act (edit/write) or finish.)",
		key,
	), true
}

func (s *thrashState) pathKey(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	if p == "" || p == "." || p == ".." {
		return ""
	}
	if strings.HasPrefix(p, "2>") || strings.HasPrefix(p, "1>") || strings.HasPrefix(p, ">") {
		return ""
	}
	if strings.ContainsAny(p, "*?[]{}") {
		return ""
	}
	// Expand $PWD-style noise
	p = strings.ReplaceAll(p, "$(pwd)", "")
	p = filepath.Clean(p)
	if s != nil && s.workspace != "" {
		abs := p
		if !filepath.IsAbs(p) {
			abs = filepath.Join(s.workspace, p)
		}
		if rel, err := filepath.Rel(s.workspace, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			p = rel
		} else if filepath.IsAbs(p) {
			ws := s.workspace
			if strings.HasPrefix(p, ws+string(filepath.Separator)) {
				p = strings.TrimPrefix(p, ws+string(filepath.Separator))
			}
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// inventoryKey groups repeated listing/status commands that thrash without
// changing code. Empty = not inventory (path-viewer or other).
func inventoryKey(cmd string) string {
	c := normalizeBashCmd(cmd)
	low := strings.ToLower(c)
	// File viewers are handled by path dedupe, not inventory class.
	if len(bashReadPaths(cmd)) > 0 && !looksLikeListing(low) {
		// pure cat/sed of files — path key only
		if !strings.Contains(low, "git ") && !strings.Contains(low, "ls") && !strings.Contains(low, "find ") {
			return ""
		}
	}
	switch {
	case strings.Contains(low, "git status"):
		return "git-status"
	case strings.Contains(low, "git log"):
		return "git-log"
	case strings.Contains(low, "git show"):
		return "git-show"
	case strings.Contains(low, "git diff"):
		return "git-diff"
	case strings.Contains(low, "git branch"):
		return "git-branch"
	case looksLikeFind(low):
		return "find"
	case looksLikeLS(low):
		return "ls"
	case strings.Contains(low, "tree "):
		return "tree"
	case strings.TrimSpace(low) == "pwd" || strings.HasPrefix(low, "pwd;") || strings.HasPrefix(low, "pwd "):
		return "pwd"
	default:
		return ""
	}
}

func looksLikeLS(low string) bool {
	// "ls", "ls -la", "ls foo", not "pulse" etc.
	fields := strings.Fields(low)
	for _, f := range fields {
		if f == "ls" || strings.HasPrefix(f, "ls ") {
			return true
		}
		if f == "ls" {
			return true
		}
	}
	for i, f := range fields {
		if f == "ls" {
			return true
		}
		// ls often first after cd
		if i > 0 && fields[i-1] != "cd" && f == "ls" {
			return true
		}
	}
	return len(fields) > 0 && fields[0] == "ls" || strings.Contains(low, " ls ") || strings.HasSuffix(low, " ls") || strings.HasPrefix(low, "ls ") || low == "ls"
}

func looksLikeFind(low string) bool {
	return strings.Contains(low, "find ") || strings.HasPrefix(low, "find ") || low == "find"
}

func looksLikeListing(low string) bool {
	return looksLikeLS(low) || looksLikeFind(low) || strings.Contains(low, "git status") || strings.Contains(low, "git log")
}

func normalizeBashCmd(cmd string) string {
	s := strings.Join(strings.Fields(cmd), " ")
	// Strip common cd prefixes so variants collide for inventory/repeat.
	for _, prefix := range []string{
		`cd "$(pwd)" && `, `cd "$(pwd)"&&`, `cd . && `, `cd .&&`,
		`cd "$(git rev-parse --show-toplevel)" && `,
		`cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" && `,
		`cd /workspace 2>/dev/null || cd .; `,
		`cd /workspace 2>/dev/null || pwd; `,
	} {
		s = strings.ReplaceAll(s, prefix, "")
	}
	// cd /abs/path &&  or  cd /abs/path;
	if strings.HasPrefix(s, "cd ") {
		if i := strings.Index(s, "&&"); i > 0 {
			s = strings.TrimSpace(s[i+2:])
		} else if i := strings.Index(s, ";"); i > 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

// bashReadPaths extracts file paths from common bash file-viewer patterns.
func bashReadPaths(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	segs := splitShellSegments(cmd)
	var out []string
	seen := map[string]bool{}
	cwd := ""
	for _, seg := range segs {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		// Track cd for relative path resolution in later segments.
		if strings.ToLower(fields[0]) == "cd" && len(fields) >= 2 {
			dir := strings.Trim(fields[1], `"'`)
			if dir != "" && dir != "." {
				cwd = dir
			}
			if len(fields) == 2 {
				continue
			}
			// cd dir && rest may appear as one Fields list with && as token
		}
		for i := 0; i < len(fields); i++ {
			op := strings.ToLower(fields[i])
			switch op {
			case "cat", "head", "tail", "bat", "less", "more", "nl", "wc":
				i++
				i = skipFlags(fields, i, map[string]bool{"-n": true, "-c": true, "-q": true, "-v": true, "-l": true})
				for i < len(fields) && !isShellOp(fields[i]) {
					p := resolveBashPath(cwd, fields[i])
					if p != "" && !seen[p] {
						seen[p] = true
						out = append(out, p)
					}
					i++
				}
				i--
			case "sed":
				i++
				for i < len(fields) && strings.HasPrefix(fields[i], "-") && fields[i] != "--" {
					i++
				}
				if i < len(fields) && fields[i] == "--" {
					i++
				}
				if i < len(fields) && !isShellOp(fields[i]) {
					i++ // script
				}
				for i < len(fields) && !isShellOp(fields[i]) {
					p := resolveBashPath(cwd, fields[i])
					if p != "" && !seen[p] {
						seen[p] = true
						out = append(out, p)
					}
					i++
				}
				i--
			case "grep", "egrep", "fgrep", "rg":
				i++
				i = skipFlags(fields, i, map[string]bool{
					"-e": true, "-f": true, "-m": true, "-A": true, "-B": true, "-C": true,
					"--include": true, "--exclude": true, "--exclude-dir": true,
				})
				// pattern then paths
				if i < len(fields) && !isShellOp(fields[i]) {
					i++ // pattern
				}
				for i < len(fields) && !isShellOp(fields[i]) {
					p := resolveBashPath(cwd, fields[i])
					if p != "" && !seen[p] {
						seen[p] = true
						out = append(out, p)
					}
					i++
				}
				i--
			}
		}
	}
	return out
}

func resolveBashPath(cwd, p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	if p == "" || isShellOp(p) {
		return ""
	}
	if strings.HasPrefix(p, "-") {
		return ""
	}
	if cwd != "" && !filepath.IsAbs(p) {
		return filepath.Join(cwd, p)
	}
	return p
}

func splitShellSegments(cmd string) []string {
	repl := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	raw := repl.Replace(cmd)
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

func isShellOp(s string) bool {
	switch s {
	case "&&", "||", ";", "|", "&":
		return true
	default:
		return false
	}
}

func skipFlags(fields []string, i int, valueFlags map[string]bool) int {
	for i < len(fields) {
		f := fields[i]
		if f == "--" {
			return i + 1
		}
		if !strings.HasPrefix(f, "-") || f == "-" {
			return i
		}
		if valueFlags[f] {
			i += 2
			continue
		}
		i++
	}
	return i
}

func (s *thrashState) annotateRepeat(name string, args json.RawMessage, out string) string {
	if s == nil || name == "read" {
		return out
	}
	if name == "bash" {
		cmd := toolArgString(args, "command")
		if !isExploreBash(cmd) {
			return out
		}
	} else if name != "glob" && name != "grep" {
		return out
	}
	fp := name + "=" + normalizeArgsFP(name, args)
	s.mu.Lock()
	n := s.calls[fp]
	s.calls[fp] = n + 1
	s.mu.Unlock()
	if n < 1 {
		return out
	}
	return out + fmt.Sprintf(
		"\n\n(note: identical %s call already ran %d time(s) this prompt — "+
			"do not repeat; change approach or finish — prefer edit/write.)",
		name, n+1,
	)
}

func normalizeArgsFP(name string, args json.RawMessage) string {
	s := strings.Join(strings.Fields(string(args)), " ")
	if name == "bash" {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			if cmd, _ := m["command"].(string); cmd != "" {
				return normalizeBashCmd(cmd)
			}
		}
		s = normalizeBashCmd(s)
	}
	return s
}

func toolArgString(args json.RawMessage, key string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func exploreWarnMessage(streak int) string {
	return fmt.Sprintf(
		"Note: %d consecutive explore-only turns (read/grep/glob/ls/cat/git status). "+
			"Stop surveying — next tools MUST be edit or write (or finish with a concrete blocker). "+
			"Re-listing and re-catting the same tree will be stubbed. Soft signal; run continues but act now.",
		streak,
	)
}

func sameToolWarnMessage(n int) string {
	return fmt.Sprintf(
		"You repeated the same tool call(s) %d times. Change args, act (edit/write), or finish — "+
			"the run is not stopped; avoid tight loops.",
		n,
	)
}
