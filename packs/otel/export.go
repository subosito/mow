package otel

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// ExportConfig is the host/user knob for the optional OTLP exporter.
// Zero value means disabled — no SDK, no network. Both Enabled and Endpoint
// are required to start the exporter.
//
// Typical YAML ($MOW_HOME/config.yaml, not project .mow/config):
//
//	otel:
//	  enabled: true
//	  endpoint: http://127.0.0.1:4318   # empty = off
//	  protocol: http                    # http (default) | grpc (reserved)
//	  service_name: mow
//	  headers:
//	    authorization: Bearer …
type ExportConfig struct {
	Enabled     bool
	Endpoint    string
	Protocol    string // "http" (default) or "grpc"
	ServiceName string
	Headers     map[string]string
}

// Export is a live OTLP pipeline: Adapter + providers. Call Shutdown when the
// process/engine exits (Engine.RegisterCleanup does this for auto-wire).
type Export struct {
	Adapter *Adapter
	tp      *sdktrace.TracerProvider
	mp      *sdkmetric.MeterProvider

	shutdownOnce sync.Once
	shutdownErr  error
}

// StartExport builds Tracer/Meter providers that push to the OTLP endpoint and
// an Adapter bound to them. Returns (nil, nil) unless cfg.Enabled is true and
// cfg.Endpoint is non-empty, so callers can treat disabled as a soft no-op.
func StartExport(ctx context.Context, cfg ExportConfig) (*Export, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if !cfg.Enabled || endpoint == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	proto := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	if proto == "" || proto == "http/protobuf" {
		proto = "http"
	}
	if proto != "http" {
		// gRPC path can land later without changing the config shape.
		return nil, fmt.Errorf("otel: protocol %q not supported yet (use http)", cfg.Protocol)
	}

	host, urlPath, insecure, err := splitOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	svc := strings.TrimSpace(cfg.ServiceName)
	if svc == "" {
		svc = "mow"
	}
	if n := utf8.RuneCountInString(svc); n > maxServiceNameRunes {
		svc = string([]rune(svc)[:maxServiceNameRunes])
	}

	headers := copyHeaders(cfg.Headers)
	applyEndpointUserinfo(headers, endpoint)

	// Schemaless merge avoids Default() vs pinned semconv SchemaURL conflicts
	// across OTel SDK bumps.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(svc),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(host),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(10 * time.Second),
	}
	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(host),
		otlpmetrichttp.WithHeaders(headers),
		otlpmetrichttp.WithTimeout(10 * time.Second),
	}
	if insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}
	if urlPath != "" {
		traceOpts = append(traceOpts, otlptracehttp.WithURLPath(joinOTLPPath(urlPath, "v1/traces")))
		metricOpts = append(metricOpts, otlpmetrichttp.WithURLPath(joinOTLPPath(urlPath, "v1/metrics")))
	}

	texp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("otel trace exporter: %w", err)
	}
	mexp, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		_ = texp.Shutdown(ctx)
		return nil, fmt.Errorf("otel metric exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(texp,
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp,
			sdkmetric.WithInterval(15*time.Second),
		)),
		sdkmetric.WithResource(res),
	)

	// Set the global propagator for hosts that also do outbound OTel; mow
	// itself correlates via RunID on the event bus. Refcounted so concurrent
	// Exports restore the previous propagator only when the last shuts down.
	installPropagator()

	ad, err := New(Options{
		Tracer: tp.Tracer("mow"),
		Meter:  mp.Meter("mow"),
	})
	if err != nil {
		restorePropagator()
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	return &Export{
		Adapter: ad,
		tp:      tp,
		mp:      mp,
	}, nil
}

// propagator refs guard the global TextMapPropagator across concurrent
// Exports: the first install captures the previous propagator, the last
// Shutdown restores it. Intermediate shutdowns leave the active value alone.
var (
	propMu   sync.Mutex
	propRefs int
	propPrev propagation.TextMapPropagator
)

func installPropagator() {
	propMu.Lock()
	defer propMu.Unlock()
	if propRefs == 0 {
		propPrev = otel.GetTextMapPropagator()
		otel.SetTextMapPropagator(propagation.TraceContext{})
	}
	propRefs++
}

func restorePropagator() {
	propMu.Lock()
	defer propMu.Unlock()
	propRefs--
	if propRefs <= 0 {
		propRefs = 0
		if propPrev != nil {
			otel.SetTextMapPropagator(propPrev)
			propPrev = nil
		}
	}
}

// Shutdown flushes and stops providers. Safe on nil and idempotent.
func (e *Export) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		if e.Adapter != nil {
			e.Adapter.Close()
		}
		var first error
		if e.tp != nil {
			if err := e.tp.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if e.mp != nil {
			if err := e.mp.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		restorePropagator()
		e.shutdownErr = first
	})
	return e.shutdownErr
}

// ForceFlush exports queued spans and metrics without shutting down. Safe on nil.
func (e *Export) ForceFlush(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var first error
	if e.tp != nil {
		if err := e.tp.ForceFlush(ctx); err != nil && first == nil {
			first = err
		}
	}
	if e.mp != nil {
		if err := e.mp.ForceFlush(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

const maxServiceNameRunes = 128

func copyHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func hasAuthHeader(h map[string]string) bool {
	for k := range h {
		if strings.EqualFold(k, "authorization") {
			return true
		}
	}
	return false
}

// applyEndpointUserinfo copies URL userinfo into Authorization when the
// operator put credentials in the endpoint and did not set a header.
func applyEndpointUserinfo(headers map[string]string, endpoint string) {
	if hasAuthHeader(headers) {
		return
	}
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	if user == "" && pass == "" {
		return
	}
	token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	headers["Authorization"] = "Basic " + token
}

// splitOTLPEndpoint parses base URLs like "http://127.0.0.1:4318" or
// "https://otlp.example.com:4318/otlp" into host:port, optional path prefix,
// and insecure flag. Bare "host:port" is treated as http.
func splitOTLPEndpoint(raw string) (host, path string, insecure bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false, fmt.Errorf("otel: empty endpoint")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false, fmt.Errorf("otel: endpoint: %w", err)
	}
	if u.Host == "" {
		return "", "", false, fmt.Errorf("otel: endpoint missing host")
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		insecure = true
	case "https":
		insecure = false
	default:
		return "", "", false, fmt.Errorf("otel: endpoint scheme %q (want http or https)", u.Scheme)
	}
	path = strings.TrimSuffix(u.Path, "/")
	// Collector default already serves /v1/traces at root; path prefix only
	// when the user put one in the URL.
	if path == "/" {
		path = ""
	}
	return u.Host, path, insecure, nil
}

func joinOTLPPath(prefix, suffix string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	suffix = strings.TrimPrefix(suffix, "/")
	if prefix == "" {
		return "/" + suffix
	}
	return prefix + "/" + suffix
}
