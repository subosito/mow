// Package ops is continuous fleet ops: named profiles under
// $MOW_HOME/ops/<name>/ (config.yaml, prompt.md, incidents/), bounded log
// reads, allowlisted restart/status, ACP peers for code fixes, and mow ops run.
// Each run tick is meant to detect issues and remediate (not only classify logs).
//
//	import _ "github.com/subosito/mow/ext/ops"
//
// Profile name is always explicit: mow ops run NAME, tool arg ops=, or MOW_OPS.
// Pack settings (root, default log caps): extensions.ops.
package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterTool(servicesTool{})
	ext.RegisterTool(logsTool{})
	ext.RegisterTool(actionTool{})
	ext.RegisterTool(incidentTool{})
	ext.RegisterCommand(ext.Command{
		Name:    "ops",
		Summary: "Fleet ops — monitor, fix via peers | list | run | …",
		Run:     opsCmd,
	})
	// BeforeNew: when MOW_OPS is set, apply profile LLM env + merge ACP peers
	// so the ops unit is self-contained (no extensions.acp required).
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		name := strings.TrimSpace(os.Getenv("MOW_OPS"))
		if name == "" {
			return nil
		}
		pack := loadPackConfig(nil)
		p, err := loadProfile(name, pack)
		if err != nil {
			return nil // profile optional at New; tools will error clearly
		}
		applyProfileLLMEnv(p)
		registerProfileACP(p, p.Workspace)
		return nil
	})
	// SessionStart: prompt.md + catalog when MOW_OPS is set.
	ext.RegisterSessionStart(func(ctx context.Context, e ext.SessionStartEvent) (ext.SessionStartDecision, error) {
		name := strings.TrimSpace(os.Getenv("MOW_OPS"))
		if name == "" {
			return ext.SessionStartDecision{}, nil
		}
		pack := loadPackConfig(nil)
		p, err := loadProfile(name, pack)
		if err != nil {
			return ext.SessionStartDecision{}, nil
		}
		registerProfileACP(p, e.Workspace)
		return ext.SessionStartDecision{SystemAppend: p.systemAppend()}, nil
	})
}

// loadProfileForTool resolves ops name + loads profile + pack defaults.
func loadProfileForTool(eng *mow.Engine, opsArg string) (Profile, PackConfig, error) {
	name, err := resolveOpsName(opsArg)
	if err != nil {
		return Profile{}, PackConfig{}, err
	}
	pack := loadPackConfig(eng)
	p, err := loadProfile(name, pack)
	return p, pack, err
}

// --- tools ---

type servicesTool struct{}

func (servicesTool) Name() string   { return "ops_services" }
func (servicesTool) ReadOnly() bool { return true }
func (servicesTool) Description() string {
	return "List services in a named ops profile (logs, actions, acp peer). Args: ops (profile name; or set MOW_OPS)."
}
func (servicesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string","description":"ops profile name (required unless MOW_OPS set)"}}}`)
}
func (servicesTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	var a struct {
		Ops string `json:"ops"`
	}
	_ = json.Unmarshal(args, &a)
	p, _, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	if len(p.Services) == 0 {
		return fmt.Sprintf("ops=%s: no services in config.yaml", p.Name), nil
	}
	type row struct {
		Name    string   `json:"name"`
		Logs    []string `json:"logs,omitempty"`
		Restart []string `json:"actions_restart,omitempty"`
		Status  []string `json:"actions_status,omitempty"`
		ACP     string   `json:"acp,omitempty"`
		Note    string   `json:"notes,omitempty"`
	}
	rows := make([]row, 0, len(p.Services))
	for _, s := range p.Services {
		rows = append(rows, row{
			Name: s.Name, Logs: s.Logs, Restart: s.Actions.Restart, Status: s.Actions.Status,
			ACP: s.ACP, Note: s.Notes,
		})
	}
	raw, _ := json.MarshalIndent(map[string]any{"ops": p.Name, "services": rows}, "", "  ")
	return string(raw), nil
}

type logsTool struct{}

func (logsTool) Name() string   { return "ops_logs" }
func (logsTool) ReadOnly() bool { return true }
func (logsTool) Description() string {
	return "Read recent log lines for a service in a named ops profile (file paths from catalog). Args: ops, service (required), source (catalog path; default first log), grep, max_lines."
}
func (logsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string"},"service":{"type":"string"},"source":{"type":"string"},"grep":{"type":"string"},"max_lines":{"type":"integer"}},"required":["service"]}`)
}
func (logsTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	var a struct {
		Ops, Service, Source, Grep string
		MaxLines                   int
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, pack, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	svc, ok := p.service(a.Service)
	if !ok {
		return fmt.Sprintf("error: unknown service %q in ops=%s — ops_services", a.Service, p.Name), nil
	}
	if len(svc.Logs) == 0 {
		return fmt.Sprintf("error: service %q has no logs paths", svc.Name), nil
	}
	src := strings.TrimSpace(a.Source)
	if src == "" {
		src = svc.Logs[0]
	}
	if !logPathAllowed(svc, src) {
		return fmt.Sprintf("error: path %q not in service %q logs catalog", src, svc.Name), nil
	}
	maxLines := a.MaxLines
	if maxLines <= 0 {
		maxLines = p.logMaxLines(pack)
	}
	lines, err := readLogFile(src, maxLines, p.logMaxBytes(pack))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	if g := strings.TrimSpace(a.Grep); g != "" {
		var filtered []string
		for _, ln := range lines {
			if strings.Contains(ln, g) {
				filtered = append(filtered, ln)
			}
		}
		lines = filtered
	}
	if len(lines) == 0 {
		return fmt.Sprintf("ops=%s service=%s source=%s: no lines", p.Name, svc.Name, src), nil
	}
	return fmt.Sprintf("ops=%s service=%s source=%s lines=%d\n%s", p.Name, svc.Name, src, len(lines), strings.Join(lines, "\n")), nil
}

func logPathAllowed(svc Service, path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	for _, p := range svc.Logs {
		if filepath.Clean(strings.TrimSpace(p)) == path {
			return true
		}
	}
	return false
}

func readLogFile(path string, maxLines, maxBytes int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	var start int64
	if size > int64(maxBytes) {
		start = size - int64(maxBytes)
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var all []string
	first := start > 0
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		all = append(all, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(all) > maxLines {
		all = all[len(all)-maxLines:]
	}
	return all, nil
}

type actionTool struct{}

func (actionTool) Name() string { return "ops_action" }
func (actionTool) Description() string {
	return "Run an allowlisted service action from the ops profile (actions.restart or actions.status argv — no shell). Args: ops, service, action (restart|status). Requires --allow-shell."
}
func (actionTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string"},"service":{"type":"string"},"action":{"type":"string","enum":["restart","status"]}},"required":["service","action"]}`)
}
func (actionTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	if !eng.AllowShell() {
		return "error: ops_action requires --allow-shell", nil
	}
	var a struct {
		Ops, Service, Action string
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, _, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	argv, err := p.actionArgv(a.Service, a.Action)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	// #nosec G204 — argv is only from operator-owned profile config, not model free text.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if len(text) > 16<<10 {
		text = text[:16<<10] + "…"
	}
	if err != nil {
		if text == "" {
			return fmt.Sprintf("error: ops=%s %s %s: %v", p.Name, a.Service, a.Action, err), nil
		}
		return fmt.Sprintf("error: ops=%s %s %s: %v\n%s", p.Name, a.Service, a.Action, err, text), nil
	}
	if text == "" {
		return fmt.Sprintf("ok ops=%s service=%s action=%s", p.Name, a.Service, a.Action), nil
	}
	return text, nil
}

// Incident is stored under ops/<name>/incidents/.
type Incident struct {
	ID        string    `json:"id"`
	Service   string    `json:"service,omitempty"`
	Signature string    `json:"signature"`
	Summary   string    `json:"summary"`
	Status    string    `json:"status"`
	Actions   []string  `json:"actions,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

type incidentTool struct{}

func (incidentTool) Name() string { return "ops_incident" }
func (incidentTool) Description() string {
	return "Durable work queue for a named ops profile ($MOW_HOME/ops/<name>/incidents). Use list first each tick; open only for issues that need attention (stable signature); update with notes after restarts/peer work; close when fixed or stale. Args: ops, action list|open|update|close; open needs signature+summary; update/close need id."
}
func (incidentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string"},"action":{"type":"string","enum":["list","open","update","close"]},"id":{"type":"string"},"signature":{"type":"string"},"summary":{"type":"string"},"service":{"type":"string"},"note":{"type":"string"},"status":{"type":"string"}},"required":["action"]}`)
}
func (incidentTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	var a struct {
		Ops, Action, ID, Signature, Summary, Service, Note, Status string
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, _, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	dir := p.incidentsDir()
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "list":
		return listIncidents(dir)
	case "open":
		return openIncident(dir, a.Service, a.Signature, a.Summary, a.Note)
	case "update":
		return updateIncident(dir, a.ID, a.Status, a.Note, false)
	case "close":
		return updateIncident(dir, a.ID, "closed", a.Note, true)
	default:
		return "error: action must be list|open|update|close", nil
	}
}

func listIncidents(dir string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "incidents: (none)", nil
		}
		return "error: " + err.Error(), nil
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "incidents: (none)", nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("incidents (%d)\n", len(ids)))
	for _, id := range ids {
		inc, err := readIncident(dir, id)
		if err != nil {
			b.WriteString("  " + id + " (read error)\n")
			continue
		}
		b.WriteString(fmt.Sprintf("  %s [%s] %s sig=%s\n", inc.ID, inc.Status, inc.Summary, inc.Signature))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func openIncident(dir, service, signature, summary, note string) (string, error) {
	signature = strings.TrimSpace(signature)
	summary = strings.TrimSpace(summary)
	if signature == "" || summary == "" {
		return "error: open requires signature and summary", nil
	}
	if existing := findOpenBySignature(dir, signature); existing != nil {
		if note != "" {
			existing.Actions = append(existing.Actions, time.Now().UTC().Format(time.RFC3339)+" "+note)
			existing.Updated = time.Now().UTC()
			_ = writeIncident(dir, *existing)
		}
		raw, _ := json.MarshalIndent(existing, "", "  ")
		return "existing open incident (deduped by signature):\n" + string(raw), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "error: " + err.Error(), nil
	}
	now := time.Now().UTC()
	id := now.Format("20060102T150405") + "-" + sanitizeID(signature)
	inc := Incident{
		ID: id, Service: strings.TrimSpace(service), Signature: signature,
		Summary: summary, Status: "open", Created: now, Updated: now,
	}
	if note != "" {
		inc.Actions = []string{now.Format(time.RFC3339) + " " + note}
	}
	if err := writeIncident(dir, inc); err != nil {
		return "error: " + err.Error(), nil
	}
	raw, _ := json.MarshalIndent(inc, "", "  ")
	return "opened:\n" + string(raw), nil
}

func updateIncident(dir, id, status, note string, forceClose bool) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "error: id required", nil
	}
	inc, err := readIncident(dir, id)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	now := time.Now().UTC()
	if forceClose {
		inc.Status = "closed"
	} else if s := strings.TrimSpace(status); s != "" {
		switch s {
		case "open", "mitigated", "closed":
			inc.Status = s
		default:
			return "error: status must be open|mitigated|closed", nil
		}
	}
	if note != "" {
		inc.Actions = append(inc.Actions, now.Format(time.RFC3339)+" "+note)
	}
	inc.Updated = now
	if err := writeIncident(dir, *inc); err != nil {
		return "error: " + err.Error(), nil
	}
	raw, _ := json.MarshalIndent(inc, "", "  ")
	return "updated:\n" + string(raw), nil
}

func findOpenBySignature(dir, sig string) *Incident {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		inc, err := readIncident(dir, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		if inc.Status == "open" && inc.Signature == sig {
			return inc
		}
	}
	return nil
}

func readIncident(dir, id string) (*Incident, error) {
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var inc Incident
	if err := json.Unmarshal(raw, &inc); err != nil {
		return nil, err
	}
	return &inc, nil
}

func writeIncident(dir string, inc Incident) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(inc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, inc.ID+".json"), append(raw, '\n'), 0o644)
}

func sanitizeID(sig string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(sig) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "inc"
	}
	return s
}
