package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/permstore"
	"github.com/subosito/mow/internal/proc"
)

const maxTraceBytes = 64 << 10

// Bundle writes a redacted Markdown snapshot under $MOW_HOME/traces/.
// It never copies API keys, env values, or file contents from config.yaml.
func Bundle(workspace string) (path string, err error) {
	r := Run(workspace)
	dir := filepath.Join(config.Home(), "traces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "mow-trace-" + r.At.UTC().Format("20060102T150405Z") + ".md"
	path = filepath.Join(dir, name)
	body := formatTrace(r)
	if len(body) > maxTraceBytes {
		body = body[:maxTraceBytes] + "\n…(truncated)\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func formatTrace(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# mow trace\n\n")
	fmt.Fprintf(&b, "- at: `%s`\n", r.At.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- home: `%s`\n", r.Home)
	fmt.Fprintf(&b, "- workspace: `%s`\n\n", r.Workspace)
	b.WriteString("## checks\n\n")
	for _, c := range r.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "- **%s** %s — %s\n", c.Name, mark, c.Detail)
	}
	b.WriteString("\n## remembered permissions\n\n")
	rules, err := permstore.Load(r.Workspace)
	if err != nil {
		fmt.Fprintf(&b, "_load error: %s_\n", err)
	} else if len(rules) == 0 {
		b.WriteString("_none_\n")
	} else {
		for _, rule := range rules {
			fmt.Fprintf(&b, "- `%s` %s %s %s\n", rule.ID, rule.Decision, rule.Tool, rule.Args)
		}
	}
	b.WriteString("\n## background processes\n\n")
	list, err := proc.List(proc.StoreDir(r.Home, r.Workspace))
	if err != nil || len(list) == 0 {
		b.WriteString("_none_\n")
	} else {
		for _, p := range list {
			state := "dead"
			if p.Alive {
				state = "running"
			}
			fmt.Fprintf(&b, "- `%s` pid=%d %s\n", p.ID, p.PID, state)
		}
	}
	b.WriteString("\n## note\n\n")
	b.WriteString("This file is local-only. It does not include API keys, tokens, or raw config.\n")
	return b.String()
}
