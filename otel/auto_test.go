package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
)

// Isolate from the developer's ~/.mow (config, skills, AGENTS) for the tests
// that construct a real Engine.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-home-otel-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestStrVal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "abc", "abc"},
		{"string trimmed", "  abc \n", "abc"},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"int", 4318, "4318"},
		{"bool", true, "true"},
		{"float", 1.5, "1.5"},
		{"typed nil map", map[string]string(nil), "map[]"},
		{"unicode", " 日本語 ", "日本語"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := strVal(c.in); got != c.want {
				t.Fatalf("strVal(%v)=%q want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExportConfigFromMapEdges(t *testing.T) {
	t.Parallel()

	t.Run("nil map is zero config", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(nil)
		if cfg.Endpoint != "" || cfg.Protocol != "" || cfg.ServiceName != "" || cfg.Headers != nil {
			t.Fatalf("want zero config, got %+v", cfg)
		}
	})

	t.Run("empty map is zero config", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(map[string]any{})
		if cfg.Endpoint != "" || cfg.Headers != nil {
			t.Fatalf("%+v", cfg)
		}
	})

	t.Run("missing keys stay empty", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(map[string]any{"unrelated": "x"})
		if cfg.Endpoint != "" || cfg.Protocol != "" || cfg.ServiceName != "" {
			t.Fatalf("%+v", cfg)
		}
	})

	t.Run("headers as map[string]string", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(map[string]any{
			"endpoint": "http://127.0.0.1:4318",
			"headers":  map[string]string{"authorization": "Bearer x"},
		})
		if cfg.Headers["authorization"] != "Bearer x" {
			t.Fatalf("headers=%v", cfg.Headers)
		}
	})

	t.Run("headers as map[string]any stringifies values", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(map[string]any{
			"headers": map[string]any{"a": "b", "n": 7, "t": true},
		})
		if cfg.Headers["a"] != "b" || cfg.Headers["n"] != "7" || cfg.Headers["t"] != "true" {
			t.Fatalf("headers=%v", cfg.Headers)
		}
	})

	t.Run("headers of unsupported type ignored", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(map[string]any{"headers": "not-a-map"})
		if cfg.Headers != nil {
			t.Fatalf("headers=%v want nil", cfg.Headers)
		}
	})

	t.Run("non-string scalars coerced", func(t *testing.T) {
		t.Parallel()
		cfg := exportConfigFromMap(map[string]any{
			"endpoint":     12345,
			"protocol":     "  HTTP  ",
			"service_name": " svc ",
		})
		if cfg.Endpoint != "12345" || cfg.Protocol != "HTTP" || cfg.ServiceName != "svc" {
			t.Fatalf("%+v", cfg)
		}
	})
}

func TestJoinOTLPPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prefix, suffix, want string
	}{
		{"", "v1/traces", "/v1/traces"},
		{"", "/v1/traces", "/v1/traces"},
		{"/otlp", "v1/traces", "/otlp/v1/traces"},
		{"/otlp/", "v1/metrics", "/otlp/v1/metrics"},
		{"/otlp", "/v1/metrics", "/otlp/v1/metrics"},
		{"/a/b", "v1/traces", "/a/b/v1/traces"},
		{"/", "v1/traces", "/v1/traces"},
	}
	for _, c := range cases {
		if got := joinOTLPPath(c.prefix, c.suffix); got != c.want {
			t.Errorf("joinOTLPPath(%q,%q)=%q want %q", c.prefix, c.suffix, got, c.want)
		}
	}
}

func TestSplitOTLPEndpointEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		in         string
		host, path string
		insecure   bool
		wantErr    string
	}{
		{name: "trailing slash dropped", in: "http://h:4318/", host: "h:4318", insecure: true},
		{name: "nested path kept", in: "https://h:4318/otlp/v2/", host: "h:4318", path: "/otlp/v2"},
		{name: "uppercase scheme", in: "HTTP://h:4318", host: "h:4318", insecure: true},
		{name: "https default port", in: "https://otlp.example.com", host: "otlp.example.com"},
		{name: "bare host no port", in: "collector", host: "collector", insecure: true},
		{name: "surrounding spaces", in: "  http://h:4318  ", host: "h:4318", insecure: true},
		{name: "userinfo preserved in host parse", in: "http://user:pass@h:4318", host: "h:4318", insecure: true},
		{name: "ipv6", in: "http://[::1]:4318", host: "[::1]:4318", insecure: true},

		{name: "empty", in: "", wantErr: "empty endpoint"},
		{name: "whitespace only", in: "   ", wantErr: "empty endpoint"},
		{name: "bad scheme", in: "ftp://h:4318", wantErr: "scheme"},
		{name: "grpc scheme rejected", in: "grpc://h:4317", wantErr: "scheme"},
		{name: "scheme only, no host", in: "http://", wantErr: "missing host"},
		{name: "unparseable url", in: "http://[::1", wantErr: "endpoint"},
		{name: "control char", in: "http://h:4318/\x7f", wantErr: "endpoint"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			host, path, insecure, err := splitOTLPEndpoint(c.in)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("splitOTLPEndpoint(%q): want error containing %q", c.in, c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err=%v want containing %q", err, c.wantErr)
				}
				if host != "" || path != "" || insecure {
					t.Fatalf("error path should return zero values, got %q %q %v", host, path, insecure)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != c.host || path != c.path || insecure != c.insecure {
				t.Fatalf("got (%q,%q,%v) want (%q,%q,%v)", host, path, insecure, c.host, c.path, c.insecure)
			}
		})
	}
}

func TestStartExportErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cfg     ExportConfig
		wantErr string
	}{
		{
			name:    "grpc not supported yet",
			cfg:     ExportConfig{Endpoint: "http://127.0.0.1:4317", Protocol: "grpc"},
			wantErr: "not supported yet",
		},
		{
			name:    "unknown protocol",
			cfg:     ExportConfig{Endpoint: "http://127.0.0.1:4318", Protocol: "carrier-pigeon"},
			wantErr: "not supported yet",
		},
		{
			name:    "bad endpoint scheme",
			cfg:     ExportConfig{Endpoint: "ftp://127.0.0.1:4318"},
			wantErr: "scheme",
		},
		{
			name:    "endpoint without host",
			cfg:     ExportConfig{Endpoint: "http://"},
			wantErr: "missing host",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			exp, err := StartExport(context.Background(), c.cfg)
			if err == nil {
				if exp != nil {
					_ = exp.Shutdown(context.Background())
				}
				t.Fatalf("want error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err=%v want containing %q", err, c.wantErr)
			}
			if exp != nil {
				t.Fatal("error path must not return an Export")
			}
		})
	}
}

func TestStartExportProtocolAliases(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"", "http", "HTTP", "  http  ", "http/protobuf"} {
		exp, err := StartExport(context.Background(), ExportConfig{
			Endpoint: "http://127.0.0.1:4318",
			Protocol: proto,
		})
		if err != nil {
			t.Fatalf("protocol %q: %v", proto, err)
		}
		if exp == nil || exp.Adapter == nil {
			t.Fatalf("protocol %q: nil export", proto)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exp.Shutdown(ctx)
		cancel()
	}
}

func TestStartExportBlankEndpointVariants(t *testing.T) {
	t.Parallel()
	for _, ep := range []string{"", "   ", "\t\n"} {
		exp, err := StartExport(context.Background(), ExportConfig{Endpoint: ep})
		if err != nil || exp != nil {
			t.Fatalf("endpoint %q: exp=%v err=%v want nil,nil", ep, exp, err)
		}
	}
}

func TestStartExportNilContext(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // StartExport documents nil ctx → context.Background.
	exp, err := StartExport(nil, ExportConfig{Endpoint: "http://127.0.0.1:4318"})
	if err != nil {
		t.Fatal(err)
	}
	if exp == nil {
		t.Fatal("nil export")
	}
	_ = exp.Shutdown(nil)
}

func TestStartExportServiceNameDefault(t *testing.T) {
	t.Parallel()
	// Blank service name must not error; the exporter defaults it to "mow".
	for _, svc := range []string{"", "   ", "custom-svc"} {
		exp, err := StartExport(context.Background(), ExportConfig{
			Endpoint:    "http://127.0.0.1:4318",
			ServiceName: svc,
		})
		if err != nil {
			t.Fatalf("service %q: %v", svc, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exp.Shutdown(ctx)
		cancel()
	}
}

func TestExportShutdown(t *testing.T) {
	t.Parallel()

	t.Run("nil export", func(t *testing.T) {
		t.Parallel()
		var e *Export
		if err := e.Shutdown(context.Background()); err != nil {
			t.Fatalf("nil Shutdown: %v", err)
		}
	})

	t.Run("zero export", func(t *testing.T) {
		t.Parallel()
		e := &Export{}
		if err := e.Shutdown(context.Background()); err != nil {
			t.Fatalf("zero Shutdown: %v", err)
		}
		if err := e.Shutdown(nil); err != nil {
			t.Fatalf("nil-ctx Shutdown: %v", err)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()
		// Point at a local collector we control so Shutdown's final flush has
		// somewhere to land. A hard-coded 127.0.0.1:4318 passes only on a box
		// that happens to run a collector, and fails in CI with "connection
		// refused" — this subtest is about shutting down twice safely, not
		// about delivering spans.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		exp, err := StartExport(context.Background(), ExportConfig{Endpoint: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exp.Shutdown(ctx); err != nil {
			t.Fatalf("first Shutdown: %v", err)
		}
		// Second shutdown
		_ = exp.Shutdown(ctx)
	})
}

func TestAutoWireNilEngine(t *testing.T) {
	t.Parallel()
	if err := autoWire(nil, map[string]any{"endpoint": "http://127.0.0.1:4318"}); err != nil {
		t.Fatalf("nil engine: %v", err)
	}
}

func newTestEngine(t *testing.T) *mow.Engine {
	t.Helper()
	eng, err := mow.New(mow.Options{Workspace: t.TempDir()})
	if err != nil {
		t.Skipf("engine unavailable in this environment: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestAutoWireDisabledByDefault(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{"nil config", nil},
		{"empty config", map[string]any{}},
		{"empty endpoint", map[string]any{"endpoint": ""}},
		{"whitespace endpoint", map[string]any{"endpoint": "   "}},
		{"other keys only", map[string]any{"service_name": "mow"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := newTestEngine(t)
			if err := autoWire(eng, c.raw); err != nil {
				t.Fatalf("autoWire: %v", err)
			}
			// No endpoint → no listener, so emitting must be inert.
			eng.Emit(mow.Event{Type: mow.EventRunStart, RunID: "r"})
		})
	}
}

func TestAutoWireInvalidEndpointErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{"bad scheme", map[string]any{"endpoint": "ftp://127.0.0.1:4318"}},
		{"missing host", map[string]any{"endpoint": "http://"}},
		{"unsupported protocol", map[string]any{"endpoint": "http://127.0.0.1:4317", "protocol": "grpc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := newTestEngine(t)
			err := autoWire(eng, c.raw)
			if err == nil {
				t.Fatal("want error from autoWire")
			}
			if !strings.Contains(err.Error(), "otel auto-wire") {
				t.Fatalf("err=%v want wrapped with 'otel auto-wire'", err)
			}
		})
	}
}

func TestAutoWireAttachesListenerAndCleanup(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	eng := newTestEngine(t)
	raw := map[string]any{
		"endpoint":     srv.URL,
		"protocol":     "http",
		"service_name": "mow-autowire-test",
		"headers":      map[string]any{"x-test": "1"},
	}
	if err := autoWire(eng, raw); err != nil {
		t.Fatalf("autoWire: %v", err)
	}

	ts := time.Now()
	eng.Emit(mow.Event{Type: mow.EventRunStart, RunID: "r-auto", TS: ts})
	eng.Emit(mow.Event{
		Type: mow.EventRunEnd, RunID: "r-auto", StopReason: mow.StopCompleted,
		InputTokens: 5, OutputTokens: 2, TS: ts.Add(time.Millisecond),
	})

	// Close runs the registered cleanup, which shuts down (and flushes) the
	// providers — the spans emitted above should reach the collector.
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if hits.Load() < 1 {
		t.Fatalf("expected the autowired exporter to POST at least once, got %d", hits.Load())
	}
}

func TestAutoWireRegisteredGlobally(t *testing.T) {
	t.Parallel()
	var fn any = autoWire
	if fn == nil {
		t.Fatal("autoWire is nil")
	}
}
