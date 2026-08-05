package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestLogPatternDefaults(t *testing.T) {
	t.Parallel()
	p := LogPattern{}
	if p.threshold() != 1 {
		t.Fatalf("threshold=%d", p.threshold())
	}
	if p.severity() != "warn" {
		t.Fatalf("severity=%q", p.severity())
	}
	if p.windowDur() != 0 {
		t.Fatalf("window=%v", p.windowDur())
	}
	p = LogPattern{Threshold: 5, Severity: " CRITICAL ", Window: "5m"}
	if p.threshold() != 5 || p.severity() != "critical" || p.windowDur().Minutes() != 5 {
		t.Fatalf("overrides: %+v", p)
	}
	// unknown severity falls back to warn; bad window falls back to 0
	p = LogPattern{Severity: "bogus", Window: "not-a-duration"}
	if p.severity() != "warn" || p.windowDur() != 0 {
		t.Fatalf("fallbacks: %+v", p)
	}
}

func TestCompilePatternCache(t *testing.T) {
	t.Parallel()
	re1, err := compilePattern(`ERROR \d+`)
	if err != nil {
		t.Fatal(err)
	}
	re2, err := compilePattern(`ERROR \d+`)
	if err != nil || re1 != re2 {
		t.Fatalf("expected cached instance, err=%v", err)
	}
	if _, err := compilePattern(`[bad`); err == nil {
		t.Fatal("invalid regex must error")
	}
}

func TestCheckServicePatterns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	body := "ERROR 502 upstream\nINFO ok\nERROR 500 timeout\nERROR 503 again\n"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Profile{}
	pack := PackConfig{}
	svc := Service{
		Name: "gw",
		Logs: []string{logPath},
		Patterns: []LogPattern{
			{Name: "http-5xx", Regex: `ERROR 5\d\d`, Threshold: 3, Severity: "critical"},
			{Name: "one-off", Regex: `INFO ok`, Threshold: 5},
			{Name: "bad-re", Regex: `[bad`},
			{Name: "", Regex: `x`}, // skipped: no name
		},
	}
	res := checkServicePatterns(p, pack, svc)
	if len(res) != 3 {
		t.Fatalf("results=%+v", res)
	}
	if res[0].Name != "http-5xx" || res[0].Matches != 3 || !res[0].Alert || res[0].Severity != "critical" {
		t.Fatalf("5xx=%+v", res[0])
	}
	if res[1].Alert || res[1].Matches != 1 {
		t.Fatalf("one-off=%+v", res[1])
	}
	if res[2].Err == "" {
		t.Fatalf("bad-re should carry an error: %+v", res[2])
	}
}

func TestPatternToolExec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	root := filepath.Join(home, "ops")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "app.log")
	if err := os.WriteFile(logPath, []byte("ERROR 502\nERROR 503\nINFO fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := `
services:
  - name: gw
    logs:
      - ` + logPath + `
    patterns:
      - name: http-5xx
        regex: 'ERROR 5\d\d'
        threshold: 2
        severity: critical
  - name: quiet
`
	writeProfileDir(t, root, "fleet", cfg)
	eng, err := mow.New(mow.Options{NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: ""}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxWithEngine(eng)

	// all services
	out, err := patternTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 alert(s)") || !strings.Contains(out, "ALERT service=gw pattern=http-5xx") {
		t.Fatalf("out=%s", out)
	}

	// scoped to one service
	out, _ = patternTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "gw"}))
	if !strings.Contains(out, "matches=2") {
		t.Fatalf("scoped out=%s", out)
	}

	// unknown service
	out, _ = patternTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "ghost"}))
	if !strings.Contains(out, "unknown service") {
		t.Fatalf("ghost out=%s", out)
	}

	// profile without any patterns
	out, _ = patternTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "quiet"}))
	if !strings.Contains(out, "no patterns declared") {
		t.Fatalf("quiet out=%s", out)
	}
}

func TestPatternToolNoEngine(t *testing.T) {
	out, err := patternTool{}.Exec(context.Background(), []byte(`{}`))
	if err != nil || !strings.Contains(out, "engine context") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestPatternToolMissingLogDegrades(t *testing.T) {
	cfg := `
services:
  - name: gw
    logs: [/nonexistent/app.log]
    patterns:
      - name: p1
        regex: ERROR
`
	eng, _ := newOpsEngine(t, "f", cfg)
	ctx := ctxWithEngine(eng)
	out, err := patternTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "f", "service": "gw"}))
	if err != nil {
		t.Fatal(err)
	}
	// Missing log → zero matches, not a tool error.
	if !strings.Contains(out, "matches=0") {
		t.Fatalf("out=%s", out)
	}
}
