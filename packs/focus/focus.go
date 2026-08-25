package focus

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Soft exploration helpers — no hard kill on explore streak. Long runs stop on
// model finish, ctx cancel, or MaxTurns. These:
//  1. degrade repeated views (read + bash cat/sed/head/tail of the same window)
//  2. degrade then refuse repeated inventory (git status/ls/find; git log/show/diff
//     and rg/grep keyed by args so distinct subjects do not collide)
//  3. soft-block destructive git/rm that discards WIP
//  4. treat test/build/commit bash as productive (resets explore streak)
//  5. warn every turn after exploreWarnEvery consecutive explore turns
//  6. after a successful edit/write, allow one re-read of that path
//
// Defaults preserve the pre-move core behavior exactly. Each is overridable
// via extensions.focus in config.
const (
	defaultExploreWarnEvery = 6
	defaultRereadLimit      = 1 // second access to the same view degrades (runs, capped)
	defaultInventoryLimit   = 2 // third identical inventory class degrades
	// defaultHardInventoryLimit is where degrading stops and refusal starts:
	// the model has already been told twice and repeated the listing anyway.
	defaultHardInventoryLimit = 4
	// defaultDegradedResultLimit caps a body that only earned a Notice. Big
	// enough to answer a real question, small enough that a repeat is not free.
	defaultDegradedResultLimit = 2000
)

// Config is extensions.focus. Zero values fall back to the defaults above,
// so an absent or partial section keeps stock behavior.
type Config struct {
	ExploreWarnEvery    int `yaml:"explore_warn_every"`
	RereadLimit         int `yaml:"reread_limit"`
	InventoryLimit      int `yaml:"inventory_limit"`
	HardInventoryLimit  int `yaml:"hard_inventory_limit"`
	DegradedResultLimit int `yaml:"degraded_result_limit"`
}

func (c Config) withDefaults() Config {
	if c.ExploreWarnEvery <= 0 {
		c.ExploreWarnEvery = defaultExploreWarnEvery
	}
	if c.RereadLimit <= 0 {
		c.RereadLimit = defaultRereadLimit
	}
	if c.InventoryLimit <= 0 {
		c.InventoryLimit = defaultInventoryLimit
	}
	if c.HardInventoryLimit <= 0 {
		c.HardInventoryLimit = defaultHardInventoryLimit
	}
	if c.DegradedResultLimit <= 0 {
		c.DegradedResultLimit = defaultDegradedResultLimit
	}
	return c
}

// focusState tracks per-Prompt exploration for soft hints only.
type focusState struct {
	mu        sync.Mutex
	cfg       Config
	workspace string // absolute; used to unify abs vs relative paths
	// view key → times accessed this Prompt (read tool windows + bash file views)
	reads map[string]int
	// inventory class key → times (git-status, ls, find, …)
	inv map[string]int
	// exact tool name+normalized args → times
	calls map[string]int
	// consecutive turns whose tools were all explore-only
	exploreStreak int
	// per-turn accumulation, folded by closeTurn
	turnSawAny        bool
	turnSawProductive bool
	// tool call id → degrade notice parked between PreTool and PostTool
	notices map[string]string
}

func newFocusState(workspace string, cfg Config) *focusState {
	ws, _ := filepath.Abs(strings.TrimSpace(workspace))
	return &focusState{
		workspace: ws,
		cfg:       cfg.withDefaults(),
		reads:     make(map[string]int),
		inv:       make(map[string]int),
		calls:     make(map[string]int),
	}
}

// isExploreToolName classifies one tool call as exploration (surveying) or
// productive action. Same rules as the pre-move isExploreToolCall, expressed
// over the PreTool event shape (name + raw args) instead of llm.ToolCall.
func isExploreToolName(name string, args json.RawMessage) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "glob", "grep":
		return true
	case "write", "edit":
		return false
	case "bash":
		return isExploreBash(toolArgString(args, "command"))
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

// guardRead is the verdict for one read-tool call. Same three-state idea as
// guardBash: repetition alone never refuses — the call runs, the body is
// capped, and a Notice is prepended. Distinct offset/limit windows of the
// same path are distinct views (paging is not thrash).
func (s *focusState) guardRead(args json.RawMessage) string {
	if s == nil {
		return ""
	}
	path := toolArgString(args, "path")
	if path == "" {
		return ""
	}
	key := s.readViewKey(path, toolArgInt(args, "offset"), toolArgInt(args, "limit"))
	if key == "" {
		return ""
	}
	s.mu.Lock()
	n := s.reads[key]
	s.reads[key] = n + 1
	s.mu.Unlock()
	if n < s.cfg.RereadLimit {
		return ""
	}
	return fmt.Sprintf(
		"(already read %q this prompt — content unchanged; do not re-read. "+
			"Use the earlier result, then act (edit/write) or finish.)",
		key,
	)
}

// readViewKey groups one read-tool window. Omitted offset/limit are 0 so an
// omitted first page and offset=0 share a key; a later page is a new view.
func (s *focusState) readViewKey(path string, offset, limit int) string {
	base := s.pathKey(path)
	if base == "" {
		return ""
	}
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset == 0 && limit == 0 {
		return base
	}
	return fmt.Sprintf("%s@%d:%d", base, offset, limit)
}

// bashGuard is the verdict for one bash call, decided before it runs.
//
// The three states are deliberate. A hard refusal costs a full round trip and
// teaches the model nothing, so repetition alone only earns a Notice: the
// command still runs, but the body is capped and carries a nudge. Refusal is
// reserved for calls that must not happen (destructive) or that have already
// been warned about and repeated anyway (hardInventoryLimit).
type bashGuard struct {
	// Block, when non-empty, replaces the tool result entirely.
	Block string
	// Notice, when non-empty, is prepended to a capped tool result.
	Notice string
}

func (g bashGuard) blocked() bool { return g.Block != "" }

// guardBash classifies a bash call: destructive → block, repeated inventory or
// re-view → degrade (run, cap, nudge), repeated past the hard limit → block.
func (s *focusState) guardBash(args json.RawMessage) bashGuard {
	if s == nil {
		return bashGuard{}
	}
	cmd := toolArgString(args, "command")
	if cmd == "" {
		return bashGuard{}
	}
	if isDestructiveBash(cmd) {
		return bashGuard{Block: "(blocked by harness: discarding uncommitted work is not allowed " +
			"(git checkout/restore/reset --hard, git clean, rm -rf of project trees). " +
			"Fix compile/test errors in place with edit/write; do not wipe WIP for green.)"}
	}
	if key := inventoryKey(cmd); key != "" {
		s.mu.Lock()
		n := s.inv[key]
		s.inv[key] = n + 1
		s.mu.Unlock()
		switch {
		case n >= s.cfg.HardInventoryLimit:
			return bashGuard{Block: fmt.Sprintf(
				"(inventory %q already ran %d times this prompt and was already warned — "+
					"refusing to re-list the tree. Act with edit/write, or finish with a "+
					"concrete blocker.)",
				key, n+1,
			)}
		case n >= s.cfg.InventoryLimit:
			return bashGuard{Notice: fmt.Sprintf(
				"(note: inventory %q already ran %d times this prompt — output capped. "+
					"Prefer grep/glob/read for targeted lookups, and act with edit/write "+
					"once the touch points are clear.)",
				key, n+1,
			)}
		}
	}
	paths := bashReadPaths(cmd)
	if len(paths) == 0 {
		return bashGuard{}
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
		if n < s.cfg.RereadLimit {
			allSeen = false
		}
	}
	if len(keys) == 0 || !allSeen {
		return bashGuard{}
	}
	shown := keys
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return bashGuard{Notice: fmt.Sprintf(
		"(note: already viewed %s this prompt — output capped; content is likely unchanged. "+
			"Do not reprint it. Act with edit/write, or finish.)",
		strings.Join(shown, ", "),
	)}
}

// degradeToolResult caps a body that only earned a Notice, then prefixes the
// nudge. Capping is what makes "run it anyway" affordable: the model still
// gets the head of the real answer without paying full context for a repeat.
func (s *focusState) degradeToolResult(notice, out string) string {
	if notice == "" {
		return out
	}
	out = truncate(out, s.cfg.DegradedResultLimit)
	if strings.TrimSpace(out) == "" {
		return notice
	}
	return notice + "\n\n" + out
}

// forgetPath clears every view of this path so the next read/cat is allowed.
// Call after a successful edit/write — the on-disk contents changed.
func (s *focusState) forgetPath(path string) {
	if s == nil {
		return
	}
	key := s.pathKey(path)
	if key == "" {
		return
	}
	prefix := key + "@"
	s.mu.Lock()
	delete(s.reads, key)
	for k := range s.reads {
		if strings.HasPrefix(k, prefix) {
			delete(s.reads, k)
		}
	}
	s.mu.Unlock()
}

func (s *focusState) pathKey(p string) string {
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
//
// Tree-wide verbs (git status, ls, find) stay a single class: repeating the
// listing is the thrash. Subject-bearing verbs (git log/show/diff, rg/grep)
// append the remainder so distinct SHAs, ranges, or patterns do not collide.
func inventoryKey(cmd string) string {
	if isProductiveBash(cmd) {
		return ""
	}
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
		return inventoryClass("git-log", c, "git log")
	case strings.Contains(low, "git show"):
		return inventoryClass("git-show", c, "git show")
	case strings.Contains(low, "git diff"):
		return inventoryClass("git-diff", c, "git diff")
	case strings.Contains(low, "git branch"):
		return "git-branch"
	case looksLikeFind(low):
		return "find"
	case looksLikeSearch(low):
		if hasCmdToken(low, "rg") {
			return inventoryClass("rg", c, "rg")
		}
		return inventoryClass("grep", c, "grep")
	case looksLikePythonListing(low):
		return "python-list"
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

// inventoryClass is "kind" plus non-flag remainder after verb, so two git-show
// calls of different SHAs do not share a ledger. Flags-only remainder (or
// none) collapses to the kind, matching the tree-wide verbs.
func inventoryClass(kind, cmd, verb string) string {
	low := strings.ToLower(cmd)
	v := strings.ToLower(verb)
	i := strings.Index(low, v)
	if i < 0 {
		return kind
	}
	rest := strings.TrimSpace(cmd[i+len(verb):])
	if rest == "" {
		return kind
	}
	fields := strings.Fields(rest)
	var keep []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			continue
		}
		keep = append(keep, f)
	}
	if len(keep) == 0 {
		return kind
	}
	return kind + " " + strings.Join(keep, " ")
}

func looksLikeLS(low string) bool {
	// "ls", "ls -la", "ls foo" — an exact token, so "pulse" etc. never match.
	for _, f := range strings.Fields(low) {
		if f == "ls" {
			return true
		}
	}
	return false
}

func looksLikeFind(low string) bool {
	return strings.Contains(low, "find ") || strings.HasPrefix(low, "find ") || low == "find"
}

func looksLikeSearch(low string) bool {
	return hasCmdToken(low, "rg") || hasCmdToken(low, "grep") || hasCmdToken(low, "egrep") || hasCmdToken(low, "fgrep")
}

func looksLikePythonListing(low string) bool {
	if !hasCmdToken(low, "python") && !hasCmdToken(low, "python3") {
		return false
	}
	// pathlib itself is how agents edit files in a one-liner; only the
	// directory-walk APIs count as a listing.
	for _, p := range []string{"os.walk", "os.listdir", "os.scandir", "glob.glob", "rglob", "pathlib.path("} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

func hasCmdToken(low, name string) bool {
	for _, f := range strings.Fields(low) {
		if strings.ToLower(filepath.Base(f)) == name {
			return true
		}
	}
	return false
}

func looksLikeListing(low string) bool {
	return looksLikeLS(low) || looksLikeFind(low) || looksLikeSearch(low) ||
		looksLikePythonListing(low) || strings.Contains(low, "git status") || strings.Contains(low, "git log")
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
			case "sed", "awk":
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

func (s *focusState) annotateRepeat(name string, args json.RawMessage, out string) string {
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

// toolArgInt reads a JSON number (float64 after encoding/json). Missing or
// non-numeric is 0 — the same as an omitted read offset/limit.
func toolArgInt(args json.RawMessage, key string) int {
	if len(args) == 0 {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func exploreWarnMessage(streak int) string {
	return fmt.Sprintf(
		"Note: %d consecutive explore-only turns (read/grep/glob/ls/cat/git status). "+
			"Stop surveying — next tools MUST be edit or write (or finish with a concrete blocker). "+
			"Re-listing and re-catting the same tree will be capped. Soft signal; run continues but act now.",
		streak,
	)
}
