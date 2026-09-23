package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow/internal/policy"
)

const maxPatchBytes = 2 << 20

type applyPatchTool struct{ p *policy.Policy }

func (t *applyPatchTool) Name() string { return "apply_patch" }
func (t *applyPatchTool) Description() string {
	return "Apply one unified text patch to one file under the path jail. The patch must contain exactly one ---/+++ file pair and exact context; binary patches, creates/deletes, renames, fuzz, and multi-file patches are refused."
}
func (t *applyPatchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"patch":{"type":"string","maxLength":2097152}},"required":["patch"]}`)
}
func (t *applyPatchTool) Exec(_ context.Context, args json.RawMessage) (string, error) {
	if err := t.p.AllowTool("apply_patch"); err != nil {
		return "", err
	}
	var a struct {
		Patch string `json:"patch"`
	}
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", err
	}
	if len(a.Patch) == 0 || len(a.Patch) > maxPatchBytes || strings.IndexByte(a.Patch, 0) >= 0 {
		return "", fmt.Errorf("apply_patch: patch is empty, binary, or exceeds %d bytes", maxPatchBytes)
	}
	path, hunks, err := parseUnifiedPatch(a.Patch)
	if err != nil {
		return "", err
	}
	resolved, data, big, err := readFileJailedCapped(t.p, path, maxEditBytes)
	if err != nil {
		return "", err
	}
	if big {
		return "", fmt.Errorf("apply_patch %s: file exceeds %d bytes", path, maxEditBytes)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("apply_patch %s: target is not UTF-8 text", path)
	}
	out, err := applyHunks(string(data), hunks)
	if err != nil {
		return "", err
	}
	if out == string(data) {
		return "", fmt.Errorf("apply_patch %s: patch makes no change", path)
	}
	if _, err = writeFileJailed(t.p, path, []byte(out), 0o644); err != nil {
		return "", err
	}
	return formatReplaceDiff(workspaceRel(t.p.Workspace, resolved), string(data), out), nil
}

type patchHunk struct {
	oldStart, oldCount, newStart, newCount int
	lines                                  []string
}

func parseUnifiedPatch(raw string) (string, []patchHunk, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "--- ") || !strings.HasPrefix(lines[1], "+++ ") {
		return "", nil, fmt.Errorf("apply_patch: expected --- and +++ headers")
	}
	oldp := patchHeaderPath(strings.TrimPrefix(lines[0], "--- "))
	newp := patchHeaderPath(strings.TrimPrefix(lines[1], "+++ "))
	if oldp == "" || newp == "" || oldp == "/dev/null" || newp == "/dev/null" || oldp != newp {
		return "", nil, fmt.Errorf("apply_patch: exactly one existing file with matching paths is required")
	}
	if cleanPatchPath(newp) == "" {
		return "", nil, fmt.Errorf("apply_patch: invalid path")
	}
	path := cleanPatchPath(newp)
	var hunks []patchHunk
	for i := 2; i < len(lines); {
		if strings.HasPrefix(lines[i], "--- ") || strings.HasPrefix(lines[i], "+++ ") {
			return "", nil, fmt.Errorf("apply_patch: multiple files are not supported")
		}
		if !strings.HasPrefix(lines[i], "@@ ") {
			return "", nil, fmt.Errorf("apply_patch: unexpected line %q", lines[i])
		}
		h, err := parseHunkHeader(lines[i])
		if err != nil {
			return "", nil, err
		}
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") {
			if strings.HasPrefix(lines[i], "--- ") || strings.HasPrefix(lines[i], "+++ ") {
				return "", nil, fmt.Errorf("apply_patch: multiple files are not supported")
			}
			if lines[i] == `\ No newline at end of file` {
				return "", nil, fmt.Errorf("apply_patch: no-newline markers are not supported")
			}
			if lines[i] == "" || !strings.ContainsRune(" +-", rune(lines[i][0])) {
				return "", nil, fmt.Errorf("apply_patch: invalid hunk line")
			}
			h.lines = append(h.lines, lines[i])
			i++
		}
		hunks = append(hunks, h)
	}
	if len(hunks) == 0 {
		return "", nil, fmt.Errorf("apply_patch: no hunks")
	}
	return path, hunks, nil
}
func cleanPatchPath(path string) string {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if path == "" || path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
		return ""
	}
	return path
}

func patchHeaderPath(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\t "); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "a/")
	s = strings.TrimPrefix(s, "b/")
	return s
}
func parseHunkHeader(s string) (patchHunk, error) {
	var h patchHunk
	parts := strings.Fields(s)
	if len(parts) < 3 {
		return h, fmt.Errorf("apply_patch: invalid hunk header")
	}
	var err error
	if h.oldStart, h.oldCount, err = parseRange(parts[1], '-'); err != nil {
		return h, err
	}
	if h.newStart, h.newCount, err = parseRange(parts[2], '+'); err != nil {
		return h, err
	}
	return h, nil
}
func parseRange(s string, prefix byte) (int, int, error) {
	if len(s) < 2 || s[0] != prefix {
		return 0, 0, fmt.Errorf("apply_patch: invalid range")
	}
	v := strings.Split(strings.TrimPrefix(s, string(prefix)), ",")
	start, err := strconv.Atoi(v[0])
	if err != nil || start < 1 {
		return 0, 0, fmt.Errorf("apply_patch: invalid range")
	}
	count := 1
	if len(v) == 2 {
		count, err = strconv.Atoi(v[1])
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("apply_patch: invalid range")
		}
	} else if len(v) > 2 {
		return 0, 0, fmt.Errorf("apply_patch: invalid range")
	}
	return start, count, nil
}
func applyHunks(original string, hunks []patchHunk) (string, error) {
	hadNL := strings.HasSuffix(original, "\n")
	lines := strings.Split(strings.TrimSuffix(original, "\n"), "\n")
	if original == "" {
		lines = nil
	}
	var out []string
	cursor := 0
	for _, h := range hunks {
		start := h.oldStart - 1
		if start < cursor || start > len(lines) {
			return "", fmt.Errorf("apply_patch: hunk range is out of order or bounds")
		}
		out = append(out, lines[cursor:start]...)
		pos := start
		oldN, newN := 0, 0
		for _, line := range h.lines {
			tag, text := line[0], line[1:]
			switch tag {
			case ' ':
				if pos >= len(lines) || lines[pos] != text {
					return "", fmt.Errorf("apply_patch: context mismatch at line %d", pos+1)
				}
				out = append(out, text)
				pos++
				oldN++
				newN++
			case '-':
				if pos >= len(lines) || lines[pos] != text {
					return "", fmt.Errorf("apply_patch: deletion mismatch at line %d", pos+1)
				}
				pos++
				oldN++
			case '+':
				out = append(out, text)
				newN++
			}
		}
		if oldN != h.oldCount || newN != h.newCount {
			return "", fmt.Errorf("apply_patch: hunk count mismatch")
		}
		cursor = pos
	}
	out = append(out, lines[cursor:]...)
	result := strings.Join(out, "\n")
	if hadNL {
		result += "\n"
	}
	return result, nil
}
