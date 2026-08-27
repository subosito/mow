// Package permstore is a durable, per-workspace remember/revoke list for
// power-tool approvals. Session "always" still lives in the ACP host;

// these rules survive resume.
package permstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow/internal/config"
)

// Decision is allow or deny.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
)

// Rule is one remembered (tool, args) decision.
type Rule struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Decision  Decision  `json:"decision"`
	CreatedAt time.Time `json:"created_at"`
}

type fileDoc struct {
	Workspace string `json:"workspace"`
	Rules     []Rule `json:"rules"`
}

var mu sync.Mutex

func storePath(workspace string) string {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	sum := sha256.Sum256([]byte(ws))
	return filepath.Join(config.Home(), "perm", hex.EncodeToString(sum[:6])+".json")
}

// KeyID is a short stable id for (tool, args).
func KeyID(tool, args string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	args = strings.TrimSpace(args)
	sum := sha256.Sum256([]byte(tool + "\n" + args))
	return hex.EncodeToString(sum[:6])
}

// Load returns remembered rules for workspace (empty if none).
func Load(workspace string) ([]Rule, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked(workspace)
}

func loadLocked(workspace string) ([]Rule, error) {
	b, err := os.ReadFile(storePath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc fileDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc.Rules, nil
}

func saveLocked(workspace string, rules []Rule) error {
	ws := strings.TrimSpace(workspace)
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	doc := fileDoc{Workspace: ws, Rules: rules}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := storePath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remember upserts an allow/deny for exact (tool, args).
func Remember(workspace, tool, args string, decision Decision) (Rule, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	args = strings.TrimSpace(args)
	if tool == "" {
		return Rule{}, fmt.Errorf("tool required")
	}
	if decision != Allow && decision != Deny {
		return Rule{}, fmt.Errorf("decision must be allow or deny")
	}
	mu.Lock()
	defer mu.Unlock()
	rules, err := loadLocked(workspace)
	if err != nil {
		return Rule{}, err
	}
	id := KeyID(tool, args)
	rule := Rule{ID: id, Tool: tool, Args: args, Decision: decision, CreatedAt: time.Now().UTC()}
	replaced := false
	for i, existing := range rules {
		if existing.ID == id {
			rules[i] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		rules = append(rules, rule)
	}
	if err := saveLocked(workspace, rules); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

// Revoke removes a rule by id. ok is false when id is unknown.
func Revoke(workspace, id string) (ok bool, err error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("id required")
	}
	mu.Lock()
	defer mu.Unlock()
	rules, err := loadLocked(workspace)
	if err != nil {
		return false, err
	}
	out := rules[:0]
	for _, r := range rules {
		if r.ID == id {
			ok = true
			continue
		}
		out = append(out, r)
	}
	if !ok {
		return false, nil
	}
	return true, saveLocked(workspace, out)
}

// Lookup returns a remembered decision for exact (tool, args).
func Lookup(workspace, tool, args string) (Decision, bool, error) {
	id := KeyID(tool, args)
	rules, err := Load(workspace)
	if err != nil {
		return "", false, err
	}
	for _, r := range rules {
		if r.ID == id {
			return r.Decision, true, nil
		}
	}
	return "", false, nil
}

// FormatLists rules as a short operator table.
func FormatList(rules []Rule) string {
	if len(rules) == 0 {
		return "(no remembered permissions)"
	}
	var b strings.Builder
	for _, r := range rules {
		args := r.Args
		if args == "" {
			args = "-"
		}
		if len(args) > 60 {
			args = args[:57] + "..."
		}
		fmt.Fprintf(&b, "%s  %-5s  %-12s  %s\n", r.ID, r.Decision, r.Tool, args)
	}
	return strings.TrimRight(b.String(), "\n")
}
