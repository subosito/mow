package agent

import (
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/subosito/mow/internal/llm"
)

// Exploration checkpoints are advisory; only exact repeated evidence can stop a run.
const (
	explorationWarnRounds = 6
	explorationWarnTime   = 2 * time.Minute
	explorationHint       = "Investigation checkpoint: state your leading explanation and the evidence already found. Continue targeted investigation while it adds useful evidence; answer when the task is complete or a concrete blocker prevents progress. Avoid repeating searches or rereading overlapping file sections. A search miss alone is not a blocker; try a different relevant path or hypothesis. Do not edit files or run unrelated commands merely to continue exploring."
)

type explorationGuard struct {
	workspace   string
	tools       map[string]Tool
	nonce       string
	started     time.Time
	now         func() time.Time
	rounds, low int
	warned      bool
	lines       map[string]map[[32]byte]struct{}
	outputs     map[[32]byte]struct{}
}

// Bash inventory does not reset the guard. Recognize common validation work,
// as focus does, without adding policy checks or blocking any tool execution.
var explorationValidation = regexp.MustCompile(`\b(go (test|build|vet|generate)|cargo (test|build|check|clippy|fmt)|npm (test|run)|pnpm (test|run|build)|yarn (test|build)|pytest|python[0-9.]* -m pytest|just |make |gofmt )`)
var explorationLinePrefix = regexp.MustCompile(`^\s*[0-9]+(?::|\|[A-Za-z0-9]+\|)\s?`)

func (g *explorationGuard) note(calls []llm.ToolCall, results []llm.Message) string {
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}
	if g.started.IsZero() {
		g.started = now
	}
	if g.lines == nil {
		g.lines = make(map[string]map[[32]byte]struct{})
		g.outputs = make(map[[32]byte]struct{})
	}
	byID := make(map[string]string, len(results))
	for _, m := range results {
		byID[m.ToolCallID] = m.Content
	}
	explored, novel, productive := 0, 0, false
	for _, c := range calls {
		body := strings.TrimSpace(byID[c.ID])
		tool := g.tools[c.Function.Name]
		if tool != nil && untrustedSource(tool, c.Function.Name) {
			// Inspect only the exact harness frame; keep history and its trust boundary intact.
			frame := WrapUntrusted(g.nonce, c.Function.Name, "")
			prefix, _, _ := strings.Cut(frame, "\n")
			if inner, ok := strings.CutPrefix(body, prefix+"\n"); ok {
				if inner, ok = strings.CutSuffix(inner, "</"+UntrustedTag+">"); ok {
					body = strings.TrimSpace(inner)
				}
			}
		}
		failed := body == "" || strings.HasPrefix(body, "error:")
		var args struct {
			Path    string `json:"path"`
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
		switch c.Function.Name {
		case "read", "grep", "glob":
		case "bash":
			if !failed && explorationValidation.MatchString(args.Command) {
				productive = true
				continue
			}
		default:
			if isExplorationTool(tool) {
				break
			}
			// Successful writes and application-specific tools end an exploration
			// streak. Failed actions do not count as productive work.
			if !failed {
				productive = true
			}
			continue
		}
		explored++
		if failed || explorationEmpty(body) {
			continue
		}
		if c.Function.Name == "read" {
			path := args.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(g.workspace, path)
			}
			path = filepath.Clean(path)
			seen := g.lines[path]
			if seen == nil {
				seen = make(map[[32]byte]struct{})
				g.lines[path] = seen
			}
			fresh, total := 0, 0
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(explorationLinePrefix.ReplaceAllString(line, ""))
				if line == "" {
					continue
				}
				total++
				key := sha256.Sum256([]byte(line))
				if _, ok := seen[key]; !ok {
					fresh++
					seen[key] = struct{}{}
				}
			}
			// Mostly overlapping windows are not new evidence. Changed file
			// contents and fresh pages still count, regardless of offset/limit.
			if fresh > 0 && fresh*5 >= total {
				novel++
			}
		} else {
			// Different failed patterns or identical inventories don't become
			// progress merely because their arguments changed.
			key := sha256.Sum256([]byte(c.Function.Name + "\x00" + body))
			if _, ok := g.outputs[key]; !ok {
				g.outputs[key] = struct{}{}
				novel++
			}
		}
	}
	if productive {
		g.rounds, g.low, g.warned = 0, 0, false
		g.started = now
		return ""
	}
	if explored == 0 {
		return ""
	}
	g.rounds++
	if novel*2 < explored {
		g.low++
	} else {
		g.low = 0
	}
	if !g.warned && (now.Sub(g.started) >= explorationWarnTime || g.rounds >= explorationWarnRounds || g.low >= 2) {
		g.warned = true
		// Issue one advisory per investigation streak.
		g.low = 0
		return explorationHint
	}
	return ""
}

// Exploration classification is a liveness hint, never permission to execute.
// Tools that mutate auxiliary caches may still perform investigative work.
func isExplorationTool(tool Tool) bool {
	if t, ok := tool.(interface{ ReadOnly() bool }); ok && t.ReadOnly() {
		return true
	}
	if t, ok := tool.(interface{ Exploratory() bool }); ok {
		return t.Exploratory()
	}
	return false
}

func explorationEmpty(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	if strings.HasPrefix(lower, "(no matches)") || lower == "no matches" || lower == "no results" {
		return true
	}
	var value any
	if json.Unmarshal([]byte(body), &value) != nil {
		return false
	}
	switch v := value.(type) {
	case []any:
		return len(v) == 0
	case map[string]any:
		for _, key := range []string{"results", "matches", "hits"} {
			if items, ok := v[key].([]any); ok && len(items) == 0 {
				return true
			}
		}
	}
	return false
}
