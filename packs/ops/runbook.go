package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

// Runbooks are operator-authored prose under $PROFILE/runbooks/<name>.md.
//
// They are deliberately *not* an executor. mow already has two ways to run a
// list of steps (the agent loop, and ext/goal); a third one inside ops would
// add failure modes without adding capability, and a fixed step list cannot
// adapt when step 2 does not apply. A runbook here is operational knowledge
// the model reads and then acts on with the tools it already has.
//
// The tick prompt advertises the available names (see Profile.systemAppend),
// so the model can pull one when a signature matches something it recognizes.
const (
	// maxRunbookBytes caps a single runbook so a stray large file cannot
	// swamp the tick context.
	maxRunbookBytes = 32 << 10
	// maxRunbookList caps how many names are advertised in the system text.
	maxRunbookList = 24
)

// runbooksDir is $PROFILE/runbooks.
func (p Profile) runbooksDir() string {
	return filepath.Join(p.Dir, "runbooks")
}

// validateRunbookName rejects anything that is not a single path segment.
// Same class of bug as the incident id: the name reaches filepath.Join, and
// this tool is not gated behind --allow-shell, so an unchecked name would let
// the model read arbitrary .md files off the host.
func validateRunbookName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("runbook name required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid runbook name %q", name)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("runbook name must be a single path segment, got %q", name)
	}
	if len(name) > 128 {
		return fmt.Errorf("runbook name too long")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("runbook name %q has invalid character %q", name, r)
	}
	return nil
}

// listRunbooks returns the runbook names available in a profile (sorted, no
// .md suffix). A missing directory is not an error — runbooks are optional.
func listRunbooks(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(strings.ToLower(n), ".md") {
			continue
		}
		n = strings.TrimSuffix(n, filepath.Ext(n))
		if validateRunbookName(n) != nil {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// readRunbook returns one runbook's prose, bounded by maxRunbookBytes.
func readRunbook(dir, name string) (string, error) {
	if err := validateRunbookName(name); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	f, st, err := openRegular(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("runbook %q not found", name)
		}
		return "", err
	}
	defer f.Close()
	limit := int64(maxRunbookBytes)
	truncated := st.Size() > limit
	if !truncated {
		limit = st.Size()
	}
	raw := make([]byte, limit)
	n, err := io.ReadFull(f, raw)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	raw = raw[:n]
	body := strings.TrimSpace(string(raw))
	if truncated {
		return body + "\n…(truncated)", nil
	}
	return body, nil
}

type runbookTool struct{}

func (runbookTool) Name() string   { return "ops_runbook" }
func (runbookTool) ReadOnly() bool { return true }
func (runbookTool) Description() string {
	return "Read operator-authored remediation notes for a named ops profile ($MOW_HOME/ops/<name>/runbooks). Args: ops, action list|get; get needs name. Runbooks are guidance, not a script — follow them with the normal tools and skip steps that do not apply."
}
func (runbookTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string"},"action":{"type":"string","enum":["list","get"]},"name":{"type":"string"}},"required":["action"]}`)
}

func (runbookTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	var a struct {
		Ops    string `json:"ops"`
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, _, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	dir := p.runbooksDir()
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "list":
		names, err := listRunbooks(dir)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if len(names) == 0 {
			return fmt.Sprintf("ops=%s runbooks: (none)", p.Name), nil
		}
		return fmt.Sprintf("ops=%s runbooks (%d)\n  %s", p.Name, len(names), strings.Join(names, "\n  ")), nil
	case "get":
		body, err := readRunbook(dir, a.Name)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if body == "" {
			return fmt.Sprintf("ops=%s runbook=%s: (empty)", p.Name, a.Name), nil
		}
		return fmt.Sprintf("ops=%s runbook=%s\n%s", p.Name, a.Name, body), nil
	default:
		return "error: action must be list|get", nil
	}
}

func init() { ext.RegisterTool(runbookTool{}) }
