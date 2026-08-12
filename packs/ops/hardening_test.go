package ops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/job"
)

func TestHealthTimeoutCapped(t *testing.T) {
	h := HealthCheck{Timeout: 999}
	if h.timeoutSec() != maxHealthTimeoutSec {
		t.Fatalf("timeout=%d", h.timeoutSec())
	}
}

func TestProbeHealthIgnoresHTTPProxy(t *testing.T) {
	proxied := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	out, err := probeHealth(context.Background(), "gw", HealthCheck{URL: srv.URL + "/health", Timeout: 5})
	if err != nil {
		t.Fatal(err)
	}
	if proxied {
		t.Fatal("health probe used HTTP_PROXY")
	}
	if !strings.Contains(out, "HEALTHY service=gw") {
		t.Fatalf("out=%s", out)
	}
}

func TestReadLogFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.log")
	if err := os.WriteFile(target, []byte("password=supersecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "app.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := readLogFile(link, 10, 1<<20); err == nil {
		t.Fatal("expected symlink reject")
	}
}

func TestRedactSecretsWordBounded(t *testing.T) {
	keep := []string{
		"csrf_token=abc123xyz",
		"supersecret=notakey",
		"mytoken=value",
	}
	for _, line := range keep {
		if got := redactSecrets(line); got != line {
			t.Fatalf("false positive on %q → %q", line, got)
		}
	}
	out := redactSecrets("token=abc123xyz")
	if strings.Contains(out, "abc123xyz") || !strings.Contains(out, "[redacted]") {
		t.Fatalf("standalone token= not redacted: %q", out)
	}
}

func TestListIncidentsSkipsTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := openIncident(dir, "s", "sig", "sum", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".inc-temp.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := listIncidents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".inc") || strings.Contains(out, "read error") {
		t.Fatalf("temp file leaked into list: %s", out)
	}
}

func TestHealthCheckBlocksLinkLocalIP(t *testing.T) {
	_, err := (HealthCheck{}).hostAllowed("http://169.254.169.254/latest")
	if err == nil || !strings.Contains(err.Error(), "permitted address") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadLogFileRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("token=abc123xyz\nAuthorization: Bearer hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := readLogFile(path, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "abc123xyz") || strings.Contains(joined, "hunter2") {
		t.Fatalf("secret leaked: %q", joined)
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Fatalf("expected redaction: %q", joined)
	}
}

func TestActionArgvDeclaredCustom(t *testing.T) {
	p := Profile{Services: []Service{
		{Name: "g", Actions: ServiceActions{"reload": {"echo", "rl"}}},
	}}
	argv, err := p.actionArgv("g", "reload")
	if err != nil || len(argv) != 2 || argv[0] != "echo" {
		t.Fatalf("argv=%v err=%v", argv, err)
	}
	if _, err := p.actionArgv("g", "RELOAD"); err != nil {
		t.Fatalf("case-insensitive lookup: %v", err)
	}
	if _, err := p.actionArgv("g", "reload;rm"); err == nil {
		t.Fatal("metacharacter action name must fail")
	}
}

func TestIncidentTextCapped(t *testing.T) {
	dir := t.TempDir()
	out, err := openIncident(dir, "svc", strings.Repeat("s", maxIncidentText+50), strings.Repeat("m", maxIncidentText+50), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, strings.Repeat("s", maxIncidentText+1)) {
		t.Fatal("signature not capped")
	}
	inc := findOpenBySignature(dir, truncateRunes(strings.Repeat("s", maxIncidentText+50), maxIncidentText))
	if inc == nil {
		t.Fatal("expected open incident")
	}
	if len([]rune(inc.Signature)) > maxIncidentText+1 {
		t.Fatalf("sig len=%d", len([]rune(inc.Signature)))
	}
}

func TestValidateOpsNameHardening(t *testing.T) {
	if err := validateOpsName("ok-name"); err != nil {
		t.Fatal(err)
	}
	if err := validateOpsName("bad\nname"); err == nil {
		t.Fatal("newline")
	}
	if err := validateOpsName(strings.Repeat("a", 65)); err == nil {
		t.Fatal("too long")
	}
}

func TestLoadProfileRejectsHugeConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "huge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(strings.Repeat("x", maxProfileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile("huge", PackConfig{Root: root}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("got %v", err)
	}
}

func TestCompilePatternBounds(t *testing.T) {
	if _, err := compilePattern(""); err == nil {
		t.Fatal("empty")
	}
	if _, err := compilePattern(strings.Repeat("a", maxPatternRegexBytes+1)); err == nil {
		t.Fatal("too long")
	}
}

func TestJobInlineCompat(t *testing.T) {
	j, err := job.InlineJob("ops-fleet", "5m", "", "", "scan the fleet")
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != "ops-fleet" || j.Every != "5m" {
		t.Fatalf("%+v", j)
	}
	if _, err := job.InlineJob("ops-fleet", "nope", "", "", "scan"); err == nil {
		t.Fatal("invalid every must fail before Start")
	}
	var n int
	d := &job.Daemon{
		Schedules: []job.Job{j},
		NewEngine: func() (*mow.Engine, error) {
			n++
			return mow.New(mow.Options{
				NoSession: true,
				Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
					return mow.Message{Role: "assistant", Content: "ok"}, nil
				},
			})
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = d.Start(ctx)
	if n < 1 {
		t.Fatal("expected at least one owned engine tick")
	}
}
