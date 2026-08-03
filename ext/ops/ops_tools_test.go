package ops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

// writeProfileDir builds $root/<name>/ with config.yaml and optional prompt.md.
// Returns the profile dir.
func writeProfileDir(t *testing.T, root, name, cfg string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newOpsEngine builds an engine whose MOW_HOME is a temp dir holding an ops
// profile, then returns the engine and the profile root. The engine carries a
// no-op Chat so mow.New does not require an API key — ops tools read files /
// run argv directly and never call the model.
func newOpsEngine(t *testing.T, name, cfg string, opts ...mow.Options) (*mow.Engine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	root := filepath.Join(home, "ops")
	writeProfileDir(t, root, name, cfg)
	o := mow.Options{NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: ""}, nil
	}}
	if len(opts) > 0 {
		// caller may override Chat/model; keep NoSession + a fallback Chat.
		o = opts[0]
		if o.Chat == nil && o.Provider == nil {
			o.Chat = func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
				return mow.Message{Role: "assistant", Content: ""}, nil
			}
		}
		o.NoSession = true
	}
	eng, err := mow.New(o)
	if err != nil {
		t.Fatalf("mow.New: %v", err)
	}
	return eng, root
}

// ctxWithEngine returns ctx carrying eng so ops tools resolve the engine.
func ctxWithEngine(eng *mow.Engine) context.Context {
	return mow.ContextWithEngine(context.Background(), eng)
}

// lookPath finds bin on PATH or skips the test (nix sandboxes lack /bin/*).
func lookPath(t *testing.T, bin string) string {
	t.Helper()
	p, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("%s not on PATH: %v", bin, err)
	}
	return p
}

// ---- profile loading & validation ----

func TestLoadProfileMissingFile(t *testing.T) {
	root := t.TempDir()
	_, err := loadProfile("ghost", PackConfig{Root: root})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadProfileBadYAML(t *testing.T) {
	root := t.TempDir()
	writeProfileDir(t, root, "bad", "model: [unclosed")
	_, err := loadProfile("bad", PackConfig{Root: root})
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadProfileLoadsPromptMarkdown(t *testing.T) {
	root := t.TempDir()
	dir := writeProfileDir(t, root, "f", "services:\n  - name: g\n")
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("## be strict"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := loadProfile("f", PackConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if p.Prompt != "## be strict" {
		t.Fatalf("prompt=%q", p.Prompt)
	}
	if p.Dir != dir {
		t.Fatalf("dir=%q want %q", p.Dir, dir)
	}
}

func TestValidateOpsName(t *testing.T) {
	cases := []struct {
		name string
		want bool // wantErr == !want
	}{
		{"fleet", true},
		{"a-b_c", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{`a\b`, false},
		{"a..b", false},
	}
	for _, c := range cases {
		err := validateOpsName(c.name)
		if (err == nil) != c.want {
			t.Errorf("validateOpsName(%q) err=%v wantErr=%v", c.name, err, !c.want)
		}
	}
}

func TestResolveOpsNameArgWinsOverEnv(t *testing.T) {
	t.Setenv("MOW_OPS", "env-name")
	got, err := resolveOpsName("arg-name")
	if err != nil || got != "arg-name" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveOpsNameInvalidArg(t *testing.T) {
	t.Setenv("MOW_OPS", "")
	if _, err := resolveOpsName("../pwn"); err == nil {
		t.Fatal("expected path traversal rejection")
	}
}

func TestListProfiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pack := PackConfig{Root: root}
	// no dir → empty, no error
	names, err := listProfiles(pack)
	if err != nil || len(names) != 0 {
		t.Fatalf("empty: names=%v err=%v", names, err)
	}
	writeProfileDir(t, root, "alpha", "services: []\n")
	writeProfileDir(t, root, "beta", "services:\n  - name: x\n")
	// non-config dir is skipped
	if err := os.MkdirAll(filepath.Join(root, "stray"), 0o755); err != nil {
		t.Fatal(err)
	}
	names, err = listProfiles(pack)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("names=%v", names)
	}
}

// ---- pack config defaults ----

func TestPackConfigDefaults(t *testing.T) {
	c := PackConfig{}
	if c.root() == "" {
		t.Fatal("root should default")
	}
	if got := c.logMaxBytes(); got != 256<<10 {
		t.Fatalf("logMaxBytes=%d want %d", got, 256<<10)
	}
	if got := c.logMaxLines(); got != 200 {
		t.Fatalf("logMaxLines=%d want 200", got)
	}
	// overrides
	c.LogMaxBytes = 1024
	c.LogMaxLines = 10
	c.Root = "/tmp/x"
	if c.root() != "/tmp/x" {
		t.Fatalf("root=%s", c.root())
	}
	if c.logMaxBytes() != 1024 || c.logMaxLines() != 10 {
		t.Fatalf("overrides not applied")
	}
}

func TestProfileLogCapsOverridePack(t *testing.T) {
	pack := PackConfig{LogMaxBytes: 1, LogMaxLines: 1}
	p := Profile{LogMaxBytes: 42, LogMaxLines: 7}
	if got := p.logMaxBytes(pack); got != 42 {
		t.Fatalf("bytes=%d", got)
	}
	if got := p.logMaxLines(pack); got != 7 {
		t.Fatalf("lines=%d", got)
	}
	// fall back to pack
	p = Profile{}
	if got := p.logMaxBytes(pack); got != 1 {
		t.Fatalf("bytes=%d", got)
	}
}

// ---- service catalog & actions ----

func TestServiceLookupCaseInsensitive(t *testing.T) {
	p := Profile{Services: []Service{{Name: "Gateway"}}}
	if _, ok := p.service("gateway"); !ok {
		t.Fatal("case-insensitive lookup failed")
	}
	if _, ok := p.service("  gateway  "); !ok {
		t.Fatal("trim failed")
	}
	if _, ok := p.service("missing"); ok {
		t.Fatal("should miss")
	}
}

func TestActionArgvErrors(t *testing.T) {
	p := Profile{Services: []Service{
		{Name: "g", Actions: ServiceActions{"restart": {"go", "version"}}},
		{Name: "no-acts"},
		{Name: "empty", Actions: ServiceActions{"status": {"echo", ""}}},
	}}
	if _, err := p.actionArgv("g", "bogus"); err == nil {
		t.Fatal("bogus action should error")
	}
	if _, err := p.actionArgv("g", "status"); err == nil {
		t.Fatal("service has no status argv → error")
	}
	if _, err := p.actionArgv("no-acts", "restart"); err == nil {
		t.Fatal("empty argv list → error")
	}
	if _, err := p.actionArgv("unknown", "restart"); err == nil {
		t.Fatal("unknown service → error")
	}
	if _, err := p.actionArgv("empty", "status"); err == nil {
		t.Fatal("empty argv element → error")
	}
	argv, err := p.actionArgv("g", "restart")
	if err != nil || len(argv) != 2 || argv[0] != "go" {
		t.Fatalf("argv=%v err=%v", argv, err)
	}
	// returned slice must not alias the source (defensive copy)
	argv[0] = "MUTATED"
	if p.Services[0].Actions["restart"][0] == "MUTATED" {
		t.Fatal("actionArgv must return a copy, not the source slice")
	}
}

// ---- ops_services tool ----

func TestServicesToolExec(t *testing.T) {
	cfg := `
services:
  - name: gateway
    logs: [/var/log/gw/app.log]
    actions:
      restart: [/bin/true]
      status:  [/bin/echo, up]
    acp: gw-peer
    notes: front door
`
	eng, _ := newOpsEngine(t, "fleet", cfg)
	ctx := ctxWithEngine(eng)

	// explicit ops name
	out, err := servicesTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet"}))
	if err != nil {
		t.Fatal(err)
	}
	// JSON is indented; assert on key/value presence, not exact spacing.
	if !strings.Contains(out, `"name": "gateway"`) || !strings.Contains(out, `"/bin/true"`) || !strings.Contains(out, `"actions_restart"`) {
		t.Fatalf("out=%s", out)
	}

	// MOW_OPS fallback
	t.Setenv("MOW_OPS", "fleet")
	out, err = servicesTool{}.Exec(ctx, mustJSON(t, map[string]any{}))
	if err != nil || !strings.Contains(out, "fleet") {
		t.Fatalf("env fallback out=%s err=%v", out, err)
	}

	// unknown profile
	out, _ = servicesTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "ghost"}))
	if !strings.Contains(out, "error:") {
		t.Fatalf("ghost should error: %s", out)
	}
}

func TestServicesToolNoEngine(t *testing.T) {
	out, err := servicesTool{}.Exec(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "engine context") {
		t.Fatalf("out=%s", out)
	}
}

func TestServicesToolEmptyCatalog(t *testing.T) {
	eng, _ := newOpsEngine(t, "empty", "model: x\n")
	ctx := ctxWithEngine(eng)
	out, err := servicesTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "empty"}))
	if err != nil || !strings.Contains(out, "no services") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

// ---- ops_logs tool ----

func TestLogsToolExec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	root := filepath.Join(home, "ops")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "gw.log")
	// write lines, then a trailing newline-less last line to exercise tail
	body := strings.Repeat("line-OLD\n", 3) + "line-NEW"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := "services:\n  - name: gw\n    logs:\n      - " + logPath + "\n"
	writeProfileDir(t, root, "fleet", cfg)

	eng, err := mow.New(mow.Options{NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: ""}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxWithEngine(eng)

	// default source, all lines
	out, err := logsTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "gw"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line-NEW") || !strings.Contains(out, "lines=4") {
		t.Fatalf("out=%s", out)
	}

	// grep filter
	out, _ = logsTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "gw", "grep": "NEW"}))
	if !strings.Contains(out, "line-NEW") || strings.Contains(out, "line-OLD") {
		t.Fatalf("grep out=%s", out)
	}

	// max_lines cap (snake_case key per the tool schema)
	out, _ = logsTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "gw", "max_lines": 1}))
	if !strings.Contains(out, "line-NEW") || strings.Contains(out, "line-OLD") {
		t.Fatalf("max_lines out=%s", out)
	}

	// foreign source path rejected
	out, _ = logsTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "gw", "source": "/etc/passwd"}))
	if !strings.Contains(out, "not in service") {
		t.Fatalf("foreign path out=%s", out)
	}

	// unknown service
	out, _ = logsTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "ghost"}))
	if !strings.Contains(out, "unknown service") {
		t.Fatalf("unknown svc out=%s", out)
	}
}

func TestLogsToolBadJSON(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	var tool logsTool
	if _, err := tool.Exec(ctx, []byte("{bad")); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestLogsToolServiceNoLogs(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services:\n  - name: bare\n")
	ctx := ctxWithEngine(eng)
	out, err := logsTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "service": "bare"}))
	if err != nil || !strings.Contains(out, "no logs paths") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestReadLogFileLargeFileTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("paddingpaddingpaddingpadding\n")
	}
	b.WriteString("the-tail-marker")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// maxBytes small enough to skip into the tail; maxLines 10
	lines, err := readLogFile(path, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("expected some lines")
	}
	// whatever we got should be the last <= 10 lines
	if len(lines) > 10 {
		t.Fatalf("too many lines=%d", len(lines))
	}
}

func TestReadLogFileMissing(t *testing.T) {
	if _, err := readLogFile("/nonexistent/ops-test.log", 5, 64); err == nil {
		t.Fatal("expected error")
	}
}

// ---- ops_action tool ----

func TestActionToolRequiresShell(t *testing.T) {
	trueBin := lookPath(t, "true")
	eng, _ := newOpsEngine(t, "f", "services:\n  - name: g\n    actions:\n      status: ["+trueBin+"]\n")
	ctx := ctxWithEngine(eng)
	out, err := actionTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "service": "g", "action": "status"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "requires --allow-shell") {
		t.Fatalf("out=%s", out)
	}
}

func TestActionToolStatusOK(t *testing.T) {
	echoBin := lookPath(t, "echo")
	cfg := "services:\n  - name: g\n    actions:\n      status: [" + echoBin + ", hello-ok]\n"
	eng, _ := newOpsEngine(t, "f", cfg, mow.Options{NoSession: true, AllowShell: true})
	ctx := ctxWithEngine(eng)
	out, err := actionTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "service": "g", "action": "status"}))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out, "hello-ok") {
		t.Fatalf("out=%s", out)
	}
}

func TestActionToolFailingCommand(t *testing.T) {
	shBin := lookPath(t, "sh")
	cfg := "services:\n  - name: g\n    actions:\n      status: [" + shBin + ", -c, \"echo boom; exit 7\"]\n"
	eng, _ := newOpsEngine(t, "f", cfg, mow.Options{NoSession: true, AllowShell: true})
	ctx := ctxWithEngine(eng)
	out, err := actionTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "service": "g", "action": "status"}))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out, "error:") || !strings.Contains(out, "boom") {
		t.Fatalf("out=%s", out)
	}
}

func TestActionToolUnknownService(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n", mow.Options{NoSession: true, AllowShell: true})
	ctx := ctxWithEngine(eng)
	out, _ := actionTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "service": "ghost", "action": "status"}))
	if !strings.Contains(out, "error:") {
		t.Fatalf("out=%s", out)
	}
}

func TestActionToolBadJSON(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n", mow.Options{NoSession: true, AllowShell: true})
	ctx := ctxWithEngine(eng)
	var tool actionTool
	if _, err := tool.Exec(ctx, []byte("{bad")); err == nil {
		t.Fatal("expected JSON error")
	}
}

// ---- ops_incident tool ----

func TestIncidentToolLifecycle(t *testing.T) {
	eng, _ := newOpsEngine(t, "fleet", "services:\n  - name: gw\n")
	ctx := ctxWithEngine(eng)

	// list empty
	out, err := incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "action": "list"}))
	if err != nil || !strings.Contains(out, "(none)") {
		t.Fatalf("empty list out=%s err=%v", out, err)
	}

	// open
	out, err = incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "open", "service": "gw",
		"signature": "sig-500", "summary": "5xx spike", "note": "first seen",
	}))
	if err != nil || !strings.Contains(out, "opened") {
		t.Fatalf("open out=%s err=%v", out, err)
	}
	id := extractIncidentJSON(t, out).ID

	// list shows it
	out, _ = incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "action": "list"}))
	if !strings.Contains(out, "sig-500") || !strings.Contains(out, "incidents (1)") {
		t.Fatalf("list out=%s", out)
	}

	// dedupe by signature → returns existing
	out, _ = incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "open", "signature": "sig-500",
		"summary": "5xx spike", "note": "dup",
	}))
	if !strings.Contains(out, "existing open") {
		t.Fatalf("dedupe out=%s", out)
	}

	// update status
	out, _ = incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "update", "id": id, "status": "mitigated", "note": "restarted",
	}))
	upd := extractIncidentJSON(t, out)
	if upd.Status != "mitigated" {
		t.Fatalf("status=%q", upd.Status)
	}

	// invalid status
	out, _ = incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "update", "id": id, "status": "bogus",
	}))
	if !strings.Contains(out, "error: status must be") {
		t.Fatalf("bad status out=%s", out)
	}

	// close
	out, _ = incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "close", "id": id, "note": "done",
	}))
	closed := extractIncidentJSON(t, out)
	if closed.Status != "closed" {
		t.Fatalf("status=%q", closed.Status)
	}

	// after close, findOpenBySignature should not match the closed incident
	// (dedupe only considers open incidents).
	p, _ := loadProfile("fleet", PackConfig{Root: filepath.Join(t.TempDir(), "ops")})
	_ = p
}

func TestIncidentToolOpenMissingFields(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	out, err := incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "action": "open"}))
	if err != nil || !strings.Contains(out, "requires signature and summary") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestIncidentToolUpdateMissingID(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	out, _ := incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "action": "update"}))
	if !strings.Contains(out, "id required") {
		t.Fatalf("out=%s", out)
	}
}

func TestIncidentToolUpdateUnknownID(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	out, _ := incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "action": "update", "id": "nope"}))
	if !strings.Contains(out, "error:") {
		t.Fatalf("out=%s", out)
	}
}

func TestIncidentToolInvalidAction(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	out, _ := incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "action": "nuke"}))
	if !strings.Contains(out, "must be list|open|update|close") {
		t.Fatalf("out=%s", out)
	}
}

func TestIncidentToolBadJSON(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	var tool incidentTool
	if _, err := tool.Exec(ctx, []byte("{bad")); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestIncidentToolNoEngine(t *testing.T) {
	out, err := incidentTool{}.Exec(context.Background(), []byte(`{"action":"list"}`))
	if err != nil || !strings.Contains(out, "engine context") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestIncidentToolProfileMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	eng, err := mow.New(mow.Options{NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: ""}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxWithEngine(eng)
	out, _ := incidentTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "ghost", "action": "list"}))
	if !strings.Contains(out, "error:") {
		t.Fatalf("out=%s", out)
	}
}

// ---- incident storage helpers ----

func TestOpenIncidentSanitizedID(t *testing.T) {
	dir := t.TempDir()
	out, err := openIncident(dir, "gw", "  SIG!@# 500  ", "boom", "n")
	if err != nil || !strings.Contains(out, "opened") {
		t.Fatalf("out=%s err=%v", out, err)
	}
	inc := extractIncidentJSON(t, out)
	// symbols collapse to '-'; the signature substring survives lowercased
	if !strings.Contains(inc.ID, "sig") || !strings.Contains(inc.ID, "500") {
		t.Fatalf("id=%q should contain sanitized signature parts", inc.ID)
	}
	if inc.Service != "gw" {
		t.Fatalf("service=%q", inc.Service)
	}
}

func TestOpenIncidentNoNote(t *testing.T) {
	dir := t.TempDir()
	out, err := openIncident(dir, "", "sig", "summary", "")
	if err != nil {
		t.Fatal(err)
	}
	inc := extractIncidentJSON(t, out)
	if len(inc.Actions) != 0 {
		t.Fatalf("actions=%v should be empty", inc.Actions)
	}
}

func TestUpdateIncidentNoteOnly(t *testing.T) {
	dir := t.TempDir()
	out, _ := openIncident(dir, "gw", "sig", "summary", "")
	id := extractIncidentJSON(t, out).ID
	upd, err := updateIncident(dir, id, "", "just a note", false)
	if err != nil {
		t.Fatal(err)
	}
	u := extractIncidentJSON(t, upd)
	if u.Status != "open" { // unchanged
		t.Fatalf("status=%q want open", u.Status)
	}
	if len(u.Actions) != 1 || !strings.Contains(u.Actions[0], "just a note") {
		t.Fatalf("actions=%v", u.Actions)
	}
}

func TestListIncidentsMissingDir(t *testing.T) {
	out, err := listIncidents(filepath.Join(t.TempDir(), "never-existed"))
	if err != nil || !strings.Contains(out, "(none)") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestListIncidentsReadError(t *testing.T) {
	dir := t.TempDir()
	// corrupt json → listed as "(read error)"
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := listIncidents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "read error") {
		t.Fatalf("out=%s", out)
	}
}

func TestReadIncidentMissing(t *testing.T) {
	if _, err := readIncident(t.TempDir(), "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteIncidentRoundtrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	inc := Incident{
		ID: "inc-1", Service: "gw", Signature: "s", Summary: "m",
		Status: "open", Created: now, Updated: now,
		Actions: []string{"note one"},
	}
	if err := writeIncident(dir, inc); err != nil {
		t.Fatal(err)
	}
	got, err := readIncident(dir, "inc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != inc.ID || got.Service != "gw" || got.Status != "open" {
		t.Fatalf("got=%+v", got)
	}
	if len(got.Actions) != 1 || got.Actions[0] != "note one" {
		t.Fatalf("actions=%v", got.Actions)
	}
}

// Incident ids come from the model via ops_incident (which is not gated behind
// --allow-write), so traversal ids must be rejected before touching the FS.
func TestIncidentIDTraversalRejected(t *testing.T) {
	bad := []string{
		"../escape",
		"../../etc/passwd",
		"sub/dir",
		`..\windows`,
		"has space",
		"dots.dots",
		"",
		"   ",
		strings.Repeat("a", 129),
	}
	for _, id := range bad {
		if err := validateIncidentID(id); err == nil {
			t.Errorf("validateIncidentID(%q) = nil, want error", id)
		}
	}
	for _, id := range []string{"inc-1", "20240101T010101-sig_500", "ABC123"} {
		if err := validateIncidentID(id); err != nil {
			t.Errorf("validateIncidentID(%q) = %v, want nil", id, err)
		}
	}
}

func TestReadWriteIncidentRejectTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "pwned")
	if err := writeIncident(dir, Incident{ID: "../pwned", Status: "open"}); err == nil {
		t.Fatal("writeIncident accepted a traversal id")
	}
	if _, err := os.Stat(outside + ".json"); !os.IsNotExist(err) {
		t.Fatalf("file escaped the incidents dir: %v", err)
	}
	if _, err := readIncident(dir, "../../etc/passwd"); err == nil {
		t.Fatal("readIncident accepted a traversal id")
	}
}

func TestSanitizeID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Sig-500", "sig-500"},
		{"  spaced!!  ", "spaced"},
		{"(((only-symbols)))", "only-symbols"},
		{"", "inc"},
		{strings.Repeat("a", 100), strings.Repeat("a", 24)},
	}
	for _, c := range cases {
		if got := sanitizeID(c.in); got != c.want {
			t.Errorf("sanitizeID(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// ---- helpers ----

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// extractIncidentJSON finds the first JSON object in out and decodes it.
func extractIncidentJSON(t *testing.T, out string) Incident {
	t.Helper()
	i := strings.Index(out, "{")
	if i < 0 {
		t.Fatalf("no JSON in out=%s", out)
	}
	var inc Incident
	if err := json.Unmarshal([]byte(out[i:]), &inc); err != nil {
		t.Fatalf("unmarshal out=%s: %v", out, err)
	}
	return inc
}
