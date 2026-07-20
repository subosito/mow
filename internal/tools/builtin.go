// Package tools implements built-in mow tools.
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

type readTool struct{ p *policy.Policy }

func (t *readTool) Name() string { return "read" }
func (t *readTool) Description() string {
	if t.p != nil && t.p.Hashline {
		return "Read a UTF-8 text file under the workspace. Lines are numbered with short hashes (N:hash|text) for hashline edit. Args: path."
	}
	return "Read a UTF-8 text file under the workspace. Args: path (relative to workspace)."
}
func (t *readTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (t *readTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	path, err := t.p.ResolvePath(a.Path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
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
	if len(data) > lim {
		data = data[:lim]
		if t.p != nil && t.p.Hashline {
			return formatHashline(string(data)) + "\n…(truncated)", nil
		}
		return string(data) + "\n…(truncated)", nil
	}
	if t.p != nil && t.p.Hashline {
		return formatHashline(string(data)), nil
	}
	return string(data), nil
}

type globTool struct{ p *policy.Policy }

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "List files matching a glob under the workspace. Args: pattern (e.g. **/*.go)."
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
	matches, err := filepath.Glob(full)
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
		if len(lines) >= 500 {
			lines = append(lines, "…(truncated)")
			break
		}
	}
	if len(lines) == 0 {
		return "(no matches)", nil
	}
	return strings.Join(lines, "\n"), nil
}

type grepTool struct{ p *policy.Policy }

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search for a fixed string in files under the workspace. Args: pattern, path (optional dir/file, default .)."
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
	var out strings.Builder
	n := 0
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Size() > int64(t.p.MaxReadBytes) && t.p.MaxReadBytes > 0 {
			return nil
		}
		// skip binary-ish
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		// jail check
		if _, err := t.p.ResolvePath(path); err != nil {
			relp, rerr := filepath.Rel(t.p.Workspace, path)
			if rerr != nil {
				return nil
			}
			if _, err := t.p.ResolvePath(relp); err != nil {
				return nil
			}
		}
		lines := strings.Split(string(data), "\n")
		relp, _ := filepath.Rel(t.p.Workspace, path)
		for i, line := range lines {
			if strings.Contains(line, a.Pattern) {
				fmt.Fprintf(&out, "%s:%d:%s\n", relp, i+1, line)
				n++
				if n >= 100 {
					out.WriteString("…(truncated)\n")
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if out.Len() == 0 {
		return "(no matches)", nil
	}
	return out.String(), nil
}

type writeTool struct{ p *policy.Policy }

func (t *writeTool) Name() string { return "write" }
func (t *writeTool) Description() string {
	return "Write content to a file under the workspace (creates parent dirs). Returns path + unified diff of the change. Args: path, content."
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
	path, err := t.p.ResolvePath(a.Path)
	if err != nil {
		return "", err
	}
	rel := a.Path
	if rel == "" {
		rel = path
	}
	var old []byte
	created := false
	if b, err := os.ReadFile(path); err == nil {
		old = b
	} else if os.IsNotExist(err) {
		created = true
	} else {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	if created {
		return formatCreateDiff(rel, a.Content), nil
	}
	return formatReplaceDiff(rel, string(old), a.Content), nil
}

type editTool struct{ p *policy.Policy }

func (t *editTool) Name() string { return "edit" }
func (t *editTool) Description() string {
	if t.p != nil && t.p.Hashline {
		return "Edit a file. Prefer hashline: path + line_hash (8 hex from read) + new_string replaces that line. Or classic old_string/new_string. Returns path + diff."
	}
	return "Replace old_string with new_string in a file (first occurrence). Returns path + unified diff of the hunk. Args: path, old_string, new_string."
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
	path, err := t.p.ResolvePath(a.Path)
	if err != nil {
		return "", err
	}
	rel := a.Path
	if rel == "" {
		rel = path
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
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
			return "", fmt.Errorf("line_hash not found")
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
			return "", fmt.Errorf("old_string not found")
		}
		oldSnippet = a.OldString
		s = strings.Replace(s, a.OldString, a.NewString, 1)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return "", err
	}
	return formatEditDiff(rel, oldSnippet, a.NewString), nil
}

type bashTool struct{ p *policy.Policy }

func (t *bashTool) Name() string { return "bash" }
func (t *bashTool) Description() string {
	return "Run a shell command with cwd=workspace. Args: command."
}
func (t *bashTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}
func (t *bashTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if err := t.p.AllowTool("bash"); err != nil {
		return "", err
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command required")
	}
	timeout := 60 * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-lc", a.Command)
	cmd.Dir = t.p.Workspace
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if len(out) > 100_000 {
		out = out[:100_000] + "\n…(truncated)"
	}
	if err != nil {
		return out + "\nerror: " + err.Error(), nil
	}
	if out == "" {
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
	return "", fmt.Errorf("hashline: no line with hash %s", hash)
}
