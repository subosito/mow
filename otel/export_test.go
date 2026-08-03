package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestSplitOTLPEndpoint(t *testing.T) {
	cases := []struct {
		in              string
		host, path      string
		insecure        bool
		wantErr         bool
	}{
		{"http://127.0.0.1:4318", "127.0.0.1:4318", "", true, false},
		{"https://otlp.example.com:4318", "otlp.example.com:4318", "", false, false},
		{"http://localhost:4318/otlp", "localhost:4318", "/otlp", true, false},
		{"127.0.0.1:4318", "127.0.0.1:4318", "", true, false},
		{"", "", "", false, true},
		{"ftp://x", "", "", false, true},
	}
	for _, tc := range cases {
		h, p, insec, err := splitOTLPEndpoint(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: want err", tc.in)
			}
			continue
		}
		if err != nil || h != tc.host || p != tc.path || insec != tc.insecure {
			t.Fatalf("%q: host=%q path=%q insec=%v err=%v", tc.in, h, p, insec, err)
		}
	}
}

func TestStartExportEmptyNoop(t *testing.T) {
	exp, err := StartExport(context.Background(), ExportConfig{})
	if err != nil || exp != nil {
		t.Fatalf("exp=%v err=%v", exp, err)
	}
}

func TestStartExportHTTPSmoke(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Accept anything; OTLP protobuf body is opaque here.
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	exp, err := StartExport(context.Background(), ExportConfig{
		Endpoint:    srv.URL,
		ServiceName: "mow-test",
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp == nil || exp.Adapter == nil {
		t.Fatal("nil export")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exp.Shutdown(ctx)
	})

	ts := time.Now()
	exp.Adapter.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r-ot", TS: ts})
	exp.Adapter.OnEvent(mow.Event{
		Type: mow.EventRunEnd, RunID: "r-ot", StopReason: mow.StopCompleted,
		InputTokens: 3, OutputTokens: 1, TS: ts.Add(time.Millisecond),
	})
	// Force flush via shutdown in cleanup; give batcher a moment.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exp.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// At least one export attempt (traces and/or metrics) should hit the server.
	// Some environments may delay; require >0 after shutdown.
	if hits.Load() < 1 {
		t.Fatalf("expected OTLP HTTP hit, got %d", hits.Load())
	}
}

func TestExportConfigFromMap(t *testing.T) {
	cfg := exportConfigFromMap(map[string]any{
		"endpoint":     " http://127.0.0.1:4318 ",
		"protocol":     "http",
		"service_name": "svc",
		"sample_ratio": 0.5,
		"headers":      map[string]any{"a": "b"},
	})
	if cfg.Endpoint != "http://127.0.0.1:4318" || cfg.ServiceName != "svc" || cfg.SampleRatio != 0.5 || cfg.Headers["a"] != "b" {
		t.Fatalf("%+v", cfg)
	}
	if !strings.HasPrefix(cfg.Endpoint, "http") {
		t.Fatal(cfg.Endpoint)
	}
}
