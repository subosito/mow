package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterTool(healthTool{})
}

// HealthCheck is an HTTP health probe declared on a Service. The URL host
// must be loopback or explicitly listed in AllowedHosts (SSRF guard — the
// model never picks hosts, only runs declared probes).
type HealthCheck struct {
	URL            string            `yaml:"url"`
	Timeout        int               `yaml:"timeout"`         // seconds; default 5
	ExpectedStatus int               `yaml:"expected_status"` // default 200
	Headers        map[string]string `yaml:"headers"`
	AllowedHosts   []string          `yaml:"allowed_hosts"`
}

func (h HealthCheck) timeoutSec() int {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return 5
}

func (h HealthCheck) expectedStatus() int {
	if h.ExpectedStatus > 0 {
		return h.ExpectedStatus
	}
	return http.StatusOK
}

// hostAllowed enforces the SSRF guard: http/https only, and the host must be
// loopback or present in allowed (case-insensitive, port ignored).
func (h HealthCheck) hostAllowed(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid health url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("health url scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("health url has no host")
	}
	if isLoopbackHost(host) {
		return host, nil
	}
	lh := strings.ToLower(host)
	for _, a := range h.AllowedHosts {
		if strings.ToLower(strings.TrimSpace(a)) == lh {
			return host, nil
		}
	}
	return "", fmt.Errorf("health url host %q not allowed (not loopback and not in allowed_hosts)", host)
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type healthTool struct{}

func (healthTool) Name() string   { return "ops_health" }
func (healthTool) ReadOnly() bool { return true }
func (healthTool) Description() string {
	return "Probe a service's declared HTTP health endpoint in an ops profile. The URL comes from operator config (host must be loopback or in allowed_hosts). Args: ops, service (required). Returns healthy/unhealthy with status code and latency."
}
func (healthTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string"},"service":{"type":"string"}},"required":["service"]}`)
}
func (healthTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	var a struct {
		Ops, Service string
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, _, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	svc, ok := p.service(a.Service)
	if !ok {
		return fmt.Sprintf("error: unknown service %q in ops=%s — ops_services", a.Service, p.Name), nil
	}
	if svc.Health == nil {
		return fmt.Sprintf("error: service %q has no health check configured", svc.Name), nil
	}
	return probeHealth(ctx, svc.Name, *svc.Health)
}

// probeHealth runs one declared probe and formats the verdict.
func probeHealth(ctx context.Context, svcName string, h HealthCheck) (string, error) {
	host, err := h.hostAllowed(h.URL)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	timeout := time.Duration(h.timeoutSec()) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimSpace(h.URL), nil)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		Timeout: timeout,
		// Redirects must re-pass the allowlist: without this a declared
		// loopback probe could be bounced to an arbitrary internal host.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if _, err := h.hostAllowed(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("UNHEALTHY service=%s host=%s: %v", svcName, host, err), nil
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)
	status := "HEALTHY"
	if resp.StatusCode != h.expectedStatus() {
		status = "UNHEALTHY"
	}
	return fmt.Sprintf("%s service=%s host=%s status=%d expected=%d latency=%dms",
		status, svcName, host, resp.StatusCode, h.expectedStatus(), elapsed.Milliseconds()), nil
}
