package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Identical-tool-call detection. This is a core engine guard, not workflow
// opinion: it catches a model wedged on the exact same call and nudges it.

// sameToolWarnAfter is how many identical consecutive tool batches trigger the
// nudge. Soft: the run continues.
const sameToolWarnAfter = 3

// normalizeArgsFP renders tool args as a stable fingerprint. Bash commands are
// normalized (cd prefixes collapsed, whitespace folded) so trivially different
// spellings of the same command still collide.
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

// normalizeBashCmd folds whitespace and strips leading cd prefixes so command
// variants that do the same work share a fingerprint.
func normalizeBashCmd(cmd string) string {
	s := strings.Join(strings.Fields(cmd), " ")
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

func sameToolWarnMessage(n int) string {
	return fmt.Sprintf(
		"You repeated the same tool call(s) %d times. Use the evidence already collected. "+
			"Make a targeted check only if it can change the conclusion; otherwise report findings and uncertainty. Do not change arguments merely to repeat the same investigation.",
		n,
	)
}
