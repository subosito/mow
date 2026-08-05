package otel

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

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
// Zero value (empty Endpoint) means disabled — no SDK, no network.
//
// Typical YAML ($MOW_HOME/config.yaml, not project .mow/config):
//
//	otel:
//	  endpoint: http://127.0.0.1:4318   # empty = off
//	  protocol: http                    # http (default) | grpc (reserved)
//	  service_name: mow
//	  headers:
//	    authorization: Bearer …
type ExportConfig struct {
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
}

// StartExport builds Tracer/Meter providers that push to the OTLP endpoint and
// an Adapter bound to them. Returns (nil, nil) when cfg.Endpoint is empty so
// callers can treat "not configured" as a soft no-op.
func StartExport(ctx context.Context, cfg ExportConfig) (*Export, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
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
		otlptracehttp.WithHeaders(cfg.Headers),
	}
	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(host),
		otlpmetrichttp.WithHeaders(cfg.Headers),
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

	// Set global propagator for hosts that also do outbound OTel; mow itself
	// correlates via RunID on the event bus.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ad, err := New(Options{
		Tracer: tp.Tracer("mow"),
		Meter:  mp.Meter("mow"),
	})
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	return &Export{Adapter: ad, tp: tp, mp: mp}, nil
}

// Shutdown flushes and stops providers. Safe on nil.
func (e *Export) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
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
	return first
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
