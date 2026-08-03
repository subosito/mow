package ops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHealthCheckHostAllowed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		h       HealthCheck
		rawURL  string
		wantErr string // empty = allowed
	}{
		{"localhost", HealthCheck{}, "http://localhost:8080/health", ""},
		{"127.0.0.1", HealthCheck{}, "http://127.0.0.1/health", ""},
		{"::1", HealthCheck{}, "http://[::1]:9000/", ""},
		{"allowed host", HealthCheck{AllowedHosts: []string{"Status.Internal"}}, "http://status.internal/h", ""},
		{"foreign host", HealthCheck{}, "http://example.com/h", "not allowed"},
		{"foreign host not in allowlist", HealthCheck{AllowedHosts: []string{"other"}}, "http://example.com/h", "not allowed"},
		{"file scheme", HealthCheck{}, "file:///etc/passwd", "scheme"},
		{"no host", HealthCheck{}, "http:///path", "no host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.h.hostAllowed(c.rawURL)
			if c.wantErr == "" && err != nil {
				t.Fatalf("want allowed, err=%v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want err containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestHealthCheckDefaults(t *testing.T) {
	t.Parallel()
	h := HealthCheck{}
	if h.timeoutSec() != 5 {
		t.Fatalf("timeout=%d", h.timeoutSec())
	}
	if h.expectedStatus() != 200 {
		t.Fatalf("expected=%d", h.expectedStatus())
	}
	h = HealthCheck{Timeout: 9, ExpectedStatus: 204}
	if h.timeoutSec() != 9 || h.expectedStatus() != 204 {
		t.Fatalf("overrides not applied")
	}
}

func TestProbeHealth(t *testing.T) {
	var headerGot string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerGot = r.Header.Get("X-Probe")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	h := HealthCheck{URL: srv.URL + "/health", Headers: map[string]string{"X-Probe": "yes"}, Timeout: 5}
	out, err := probeHealth(context.Background(), "gw", h)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HEALTHY service=gw") || !strings.Contains(out, "status=200") {
		t.Fatalf("out=%s", out)
	}
	if u.Hostname() != "127.0.0.1" {
		t.Fatalf("test server host=%q", u.Hostname())
	}
	if headerGot != "yes" {
		t.Fatalf("header not forwarded: %q", headerGot)
	}

	// wrong status → UNHEALTHY
	h.ExpectedStatus = 204
	out, _ = probeHealth(context.Background(), "gw", h)
	if !strings.Contains(out, "UNHEALTHY") {
		t.Fatalf("want unhealthy: %s", out)
	}

	// SSRF guard blocks non-loopback before any dial
	out, _ = probeHealth(context.Background(), "gw", HealthCheck{URL: "http://example.com/h"})
	if !strings.Contains(out, "not allowed") {
		t.Fatalf("ssrf guard: %s", out)
	}

	// connection refused → UNHEALTHY with error text
	out, _ = probeHealth(context.Background(), "gw", HealthCheck{URL: "http://127.0.0.1:1/health", Timeout: 1})
	if !strings.Contains(out, "UNHEALTHY") {
		t.Fatalf("refused: %s", out)
	}
}

func TestHealthToolExec(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := `
services:
  - name: gw
    health:
      url: ` + srv.URL + `/health
      expected_status: 200
  - name: bare
`
	eng, _ := newOpsEngine(t, "fleet", cfg)
	ctx := ctxWithEngine(eng)

	out, err := healthTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "gw"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HEALTHY service=gw") {
		t.Fatalf("out=%s", out)
	}

	// service without health block
	out, _ = healthTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "bare"}))
	if !strings.Contains(out, "no health check configured") {
		t.Fatalf("bare: %s", out)
	}

	// unknown service
	out, _ = healthTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "service": "ghost"}))
	if !strings.Contains(out, "unknown service") {
		t.Fatalf("ghost: %s", out)
	}
}

func TestHealthToolNoEngine(t *testing.T) {
	out, err := healthTool{}.Exec(context.Background(), []byte(`{}`))
	if err != nil || !strings.Contains(out, "engine context") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestHealthToolBadJSON(t *testing.T) {
	eng, _ := newOpsEngine(t, "f", "services: []\n")
	ctx := ctxWithEngine(eng)
	if _, err := (healthTool{}).Exec(ctx, []byte("{bad")); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestHealthYAMLParse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := `
services:
  - name: gw
    health:
      url: http://localhost:9999/health
      timeout: 3
      expected_status: 204
      headers:
        X-Token: abc
      allowed_hosts: [status.lan]
    patterns: []
`
	writeProfileDir(t, root, "fleet", cfg)
	p, err := loadProfile("fleet", PackConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	h := p.Services[0].Health
	if h == nil || h.URL != "http://localhost:9999/health" || h.Timeout != 3 || h.ExpectedStatus != 204 {
		t.Fatalf("health=%+v", h)
	}
	if h.Headers["X-Token"] != "abc" || len(h.AllowedHosts) != 1 {
		t.Fatalf("health=%+v", h)
	}
}

// A loopback probe must not be usable as an open redirect into other hosts.
func TestProbeHealthBlocksRedirectOffAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/internal", http.StatusFound)
	}))
	defer srv.Close()

	out, err := probeHealth(context.Background(), "gw", HealthCheck{URL: srv.URL + "/health", Timeout: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "UNHEALTHY") || !strings.Contains(out, "redirect blocked") {
		t.Fatalf("redirect off the allowlist should fail the probe, got: %s", out)
	}
}

// Redirects that stay on an allowed host are still followed.
func TestProbeHealthFollowsAllowedRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/healthz", http.StatusFound)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, _ := probeHealth(context.Background(), "gw", HealthCheck{URL: srv.URL + "/health", Timeout: 5})
	if !strings.Contains(out, "HEALTHY service=gw") || !strings.Contains(out, "status=200") {
		t.Fatalf("out=%s", out)
	}
}
