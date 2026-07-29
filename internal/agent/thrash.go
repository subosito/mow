package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subosito/mow/internal/llm"
)

// Soft exploration helpers — no hard kill. Long autonomous runs stop on:
// model finishes, ctx cancel, or MaxTurns when the user set one.
// These (1) stub re-reads (including bash cat/sed/head/tail), (2) treat
// productive bash (test/build/commit) as non-explore, (3) nudge earlier.
const (
	// exploreWarnEvery injects a wrap-up nudge every N explore-only turns.
	exploreWarnEvery = 6
	// rereadLimit: after this many successful reads of the same path in one
	// Prompt, further reads return a short stub instead of the full file.
	rereadLimit = 1
	// sameToolWarnAfter injects a nudge when the identical tool batch repeats.
	sameToolWarnAfter = 3
)

// thrashState tracks per-Prompt exploration for soft hints only.
type thrashState struct {
	mu sync.Mutex
	// path → times successfully read this Prompt (read tool + bash file views)
	reads map[string]int
	// exact tool name+args → times (glob/bash/grep)
	calls map[string]int
	// consecutive turns whose tools were all explore-only
	exploreStreak int
}

func newThrashState() *thrashState {
	return &thrashState{
		reads: make(map[string]int),
		calls: make(map[string]int),
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

// isExploreBash reports whether a bash command is inventory/read-only thrash
// rather than a productive test/build/write side effect.
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
	// Substrings: common productive tooling. Intentionally broad.
	for _, p := range []string{
		"go test", "go build", "go vet", "go generate", "gofmt -w", "go fmt",
		"cargo test", "cargo build", "npm test", "npm run", "pnpm ", "yarn ",
		"pytest", "python -m pytest",
		"just ", "make ", "task ",
		"git add", "git commit", "git push", "git stash", "git mv", "git rm",
		"git checkout -b", "git switch -c",
	} {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

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

// noteTurn updates explore streak. Returns whether to inject a soft wrap-up warn.
func (s *thrashState) noteTurn(calls []llm.ToolCall) (warn bool) {
	if s == nil {
		return false
	}
	if !batchExploreOnly(calls) {
		s.exploreStreak = 0
		return false
	}
	s.exploreStreak++
	return s.exploreStreak > 0 && s.exploreStreak%exploreWarnEvery == 0
}

// maybeDedupeRead short-circuits repeated reads of the same path.
// Returns (result, handled). handled=true means caller should use result as tool output.
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

// maybeDedupeBash short-circuits repeated bash file viewers (cat/sed/head/tail/…)
// when every path in the command was already read this prompt.
func (s *thrashState) maybeDedupeBash(args json.RawMessage) (string, bool) {
	if s == nil {
		return "", false
	}
	cmd := toolArgString(args, "command")
	paths := bashReadPaths(cmd)
	if len(paths) == 0 {
		return "", false
	}
	// Record each path; stub only when every path was already seen.
	allSeen := true
	var keys []string
	for _, p := range paths {
		key := pathKey(p)
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
		"(already viewed %s this prompt via bash — content unchanged; do not re-cat/sed. "+
			"Use the earlier result, then act (edit/write) or finish.)",
		strings.Join(shown, ", "),
	), true
}

func (s *thrashState) notePathAccess(path string) (string, bool) {
	key := pathKey(path)
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

func pathKey(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	if p == "" || p == "." || p == ".." {
		return ""
	}
	// Drop obvious non-paths / redirects.
	if strings.HasPrefix(p, "2>") || strings.HasPrefix(p, "1>") || strings.HasPrefix(p, ">") {
		return ""
	}
	if strings.ContainsAny(p, "*?[]{}") {
		return "" // globs — not a single-file re-read key
	}
	return filepath.Clean(p)
}

// bashReadPaths extracts file paths from common bash file-viewer patterns.
// Best-effort: not a full shell parser.
func bashReadPaths(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	// Split on shell chain operators (rough).
	segs := splitShellSegments(cmd)
	var out []string
	seen := map[string]bool{}
	for _, seg := range segs {
		fields := strings.Fields(seg)
		for i := 0; i < len(fields); i++ {
			op := strings.ToLower(fields[i])
			switch op {
			case "cat", "head", "tail", "bat", "less", "more", "nl", "wc":
				i++
				i = skipFlags(fields, i, map[string]bool{"-n": true, "-c": true, "-q": true, "-v": true})
				for i < len(fields) && !isShellOp(fields[i]) {
					if p := pathKey(fields[i]); p != "" && !seen[p] {
						seen[p] = true
						out = append(out, p)
					}
					i++
				}
				i-- // loop will ++
			case "sed":
				i++
				// sed [-flags] script file…
				for i < len(fields) && strings.HasPrefix(fields[i], "-") && fields[i] != "--" {
					i++
				}
				if i < len(fields) && fields[i] == "--" {
					i++
				}
				// script expression
				if i < len(fields) && !isShellOp(fields[i]) {
					i++
				}
				for i < len(fields) && !isShellOp(fields[i]) {
					if p := pathKey(fields[i]); p != "" && !seen[p] {
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

func splitShellSegments(cmd string) []string {
	// Split on &&, ||, ;, | while keeping simple cases working.
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
		// Combined flags like -n20 or -la
		i++
	}
	return i
}

// annotateRepeat notes repeated identical name+args for non-read tools (soft footer).
func (s *thrashState) annotateRepeat(name string, args json.RawMessage, out string) string {
	if s == nil || name == "read" {
		return out
	}
	// Only annotate explore-class tools (including explore bash).
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
		// Collapse trivial cd-prefix variance for fingerprinting.
		s = strings.ReplaceAll(s, `cd "$(pwd)" && `, "")
		s = strings.ReplaceAll(s, `cd "$(pwd)"&&`, "")
		s = strings.ReplaceAll(s, "cd . && ", "")
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
		"Note: %d consecutive explore-only turns (read/grep/glob/ls/cat/status). "+
			"Stop surveying — your next tools should be edit or write (or finish with a concrete blocker). "+
			"Re-reading the same tree will not help. Soft signal only; the run continues but you must act.",
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
