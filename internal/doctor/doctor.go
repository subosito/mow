// Package doctor inspects host and workspace state without starting sessions,
// MCP servers, or background processes.
package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/proc"
	"github.com/subosito/mow/internal/tools"
)

// Check is one diagnostic row.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is a side-effect-free snapshot of this host + workspace.
type Report struct {
	At        time.Time `json:"at"`
	Home      string    `json:"home"`
	Workspace string    `json:"workspace"`
	Checks    []Check   `json:"checks"`
}

// Run inspects home + workspace. It never launches MCP, agents, or procs.
func Run(workspace string) Report {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	home := config.Home()
	r := Report{At: time.Now().UTC(), Home: home, Workspace: ws}
	r.Checks = append(r.Checks, checkHome(home))
	r.Checks = append(r.Checks, checkConfig(home))
	r.Checks = append(r.Checks, checkTools())
	r.Checks = append(r.Checks, checkAgents(home, ws))
	r.Checks = append(r.Checks, checkTrust(ws))
	r.Checks = append(r.Checks, checkMCP(ws)...)
	r.Checks = append(r.Checks, checkSkills(home, ws))
	r.Checks = append(r.Checks, checkProcStore(home, ws))
	return r
}

func checkHome(home string) Check {
	st, err := os.Stat(home)
	if err != nil {
		return Check{Name: "home", OK: false, Detail: err.Error()}
	}
	if !st.IsDir() {
		return Check{Name: "home", OK: false, Detail: home + " is not a directory"}
	}
	return Check{Name: "home", OK: true, Detail: home}
}

func checkConfig(home string) Check {
	path := filepath.Join(home, "config.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{Name: "config", OK: true, Detail: "no config.yaml (defaults)"}
		}
		return Check{Name: "config", OK: false, Detail: err.Error()}
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		// yaml, not json — try a light presence check only
		if len(strings.TrimSpace(string(b))) == 0 {
			return Check{Name: "config", OK: false, Detail: path + " is empty"}
		}
		return Check{Name: "config", OK: true, Detail: fmt.Sprintf("%s (%d B)", path, len(b))}
	}
	return Check{Name: "config", OK: true, Detail: path}
}

func checkTools() Check {
	cfg, err := config.Load()
	if err != nil {
		return Check{Name: "tools", OK: false, Detail: err.Error()}
	}
	if cfg == nil {
		return Check{Name: "tools", OK: true, Detail: "defaults (read, glob, grep)"}
	}
	have := registeredToolNames()
	miss := tools.UnknownEnable(cfg.Tools.Enable, have)
	if len(miss) == 0 {
		return Check{Name: "tools", OK: true, Detail: strings.Join(cfg.Tools.Enable, ", ")}
	}
	return Check{Name: "tools", OK: false, Detail: tools.FormatUnregisteredEnable(miss, mediaPackLinked())}
}

func registeredToolNames() []string {
	names := append([]string(nil), tools.RegistryNames()...)
	for _, t := range ext.ToolsForEngine(true) {
		if t != nil {
			names = append(names, t.Name())
		}
	}
	if mediaPackLinked() {
		names = append(names, tools.MediaEnableNames()...)
	}
	return names
}

func mediaPackLinked() bool {
	for _, f := range ext.OptionalFeatures() {
		if f.ID == "media" {
			return true
		}
	}
	return false
}

func hostBinary() string {
	if _, ok := ext.LookupCommand("goal"); ok {
		if _, ok := ext.LookupCommand("ops"); ok {
			if _, ok := ext.LookupCommand("review"); ok {
				return "mow-full"
			}
		}
	}
	return "mow"
}

func checkAgents(home, ws string) Check {
	var found []string
	for _, p := range []string{
		filepath.Join(home, "AGENTS.md"),
		filepath.Join(ws, "AGENTS.md"),
		filepath.Join(ws, "CLAUDE.md"),
	} {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			found = append(found, filepath.Base(p))
		}
	}
	if len(found) == 0 {
		return Check{Name: "instructions", OK: true, Detail: "no AGENTS.md / CLAUDE.md"}
	}
	return Check{Name: "instructions", OK: true, Detail: strings.Join(found, ", ")}
}

func checkTrust(ws string) Check {
	if config.WorkspaceTrusted(ws) {
		return Check{Name: "trust", OK: true, Detail: "workspace is trusted"}
	}
	return Check{Name: "trust", OK: true, Detail: "workspace not on trusted list"}
}

func checkMCP(ws string) []Check {
	cands := []string{
		filepath.Join(ws, "mcp.json"),
		filepath.Join(ws, ".mow", "mcp.json"),
	}
	var out []Check
	seen := false
	for _, p := range cands {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		seen = true
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			out = append(out, Check{Name: "mcp", OK: false, Detail: p + ": " + err.Error()})
			continue
		}
		n := 0
		if servers, ok := raw["mcpServers"].(map[string]any); ok {
			n = len(servers)
		} else if servers, ok := raw["servers"].(map[string]any); ok {
			n = len(servers)
		}
		out = append(out, Check{Name: "mcp", OK: true, Detail: fmt.Sprintf("%s (%d servers, not started)", p, n)})
	}
	if !seen {
		out = append(out, Check{Name: "mcp", OK: true, Detail: "no mcp.json"})
	}
	return out
}

func checkSkills(home, ws string) Check {
	var n int
	for _, dir := range []string{
		filepath.Join(home, "skills"),
		filepath.Join(ws, ".mow", "skills"),
		filepath.Join(ws, "skills"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			n++
		}
	}
	if n == 0 {
		return Check{Name: "skills", OK: true, Detail: "no skill files"}
	}
	return Check{Name: "skills", OK: true, Detail: fmt.Sprintf("%d skill entries (not loaded)", n)}
}

func checkProcStore(home, ws string) Check {
	dir := proc.StoreDir(home, ws)
	list, err := proc.List(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{Name: "proc", OK: true, Detail: "no process store"}
		}
		return Check{Name: "proc", OK: false, Detail: err.Error()}
	}
	alive := 0
	for _, p := range list {
		if p.Alive {
			alive++
		}
	}
	return Check{Name: "proc", OK: true, Detail: fmt.Sprintf("%d recorded · %d running", len(list), alive)}
}

// Format is a human report. Secrets are never copied — only paths and counts.
func Format(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mow doctor  %s\n", r.At.Format(time.RFC3339))
	fmt.Fprintf(&b, "binary     %s\n", hostBinary())
	fmt.Fprintf(&b, "home       %s\n", r.Home)
	fmt.Fprintf(&b, "workspace  %s\n", r.Workspace)
	b.WriteByte('\n')
	ok := true
	for _, c := range r.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
			ok = false
		}
		fmt.Fprintf(&b, "%-14s %-4s %s\n", c.Name, mark, c.Detail)
	}
	if !ok {
		b.WriteString("\none or more checks failed\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
