package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

const (
	defaultHealthTimeoutSec = 5
	maxHealthTimeoutSec     = 30
	maxHealthBodyBytes      = 8 << 10
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
	if h.Timeout > maxHealthTimeoutSec {
		return maxHealthTimeoutSec
	}
	if h.Timeout > 0 {
		return h.Timeout
	}
	return defaultHealthTimeoutSec
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
	if ip := net.ParseIP(host); ip != nil && blockedHealthIP(ip) {
		return "", fmt.Errorf("health url host %q is not a permitted address", host)
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

// safeHealthDial resolves addr and filters destination IPs before connect.
// loopbackOnly: only loopback IPs (localhost cannot be rebound off-box).
// otherwise: skip link-local / unspecified / multicast (metadata rebinding).
func safeHealthDial(timeout time.Duration, loopbackOnly bool) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ipa := range ips {
			ip := ipa.IP
			if loopbackOnly {
				if !ip.IsLoopback() {
					last = fmt.Errorf("resolved %s to non-loopback %s", host, ip)
					continue
				}
			} else if blockedHealthIP(ip) {
				last = fmt.Errorf("resolved %s to blocked address %s", host, ip)
				continue
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("no usable address for %s", host)
		}
		return nil, last
	}
}

func blockedHealthIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
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
	// Never honor HTTP_PROXY: a proxy would let a "loopback" probe leave the box.
	// Loopback names may only connect to loopback IPs. Allowed hosts still
	// resolve via DNS but cannot land on link-local/metadata (rebinding).
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext:       safeHealthDial(timeout, isLoopbackHost(host)),
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Redirects must re-pass the allowlist: without this a declared
		// loopback probe could be bounced to an arbitrary internal host.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			redirHost, err := h.hostAllowed(req.URL.String())
			if err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			if isLoopbackHost(host) && !isLoopbackHost(redirHost) {
				return fmt.Errorf("redirect blocked: loopback probe left loopback")
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHealthBodyBytes))
	elapsed := time.Since(start)
	status := "HEALTHY"
	if resp.StatusCode != h.expectedStatus() {
		status = "UNHEALTHY"
	}
	return fmt.Sprintf("%s service=%s host=%s status=%d expected=%d latency=%dms",
		status, svcName, host, resp.StatusCode, h.expectedStatus(), elapsed.Milliseconds()), nil
}
