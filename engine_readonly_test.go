package mow

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsReadOnlyTool(t *testing.T) {
	extRO := map[string]bool{"mcp_srv_lookup": true}
	cases := []struct {
		name string
		want bool
	}{
		{"read", true},
		{"glob", true},
		{"grep", true},
		{"understand_image", true},
		{"write", false},
		{"edit", false},
		{"bash", false},
		{"generate_image", false},
		{"mcp_srv_lookup", true},   // declared readOnlyHint
		{"mcp_srv_execute", false}, // undeclared ext tool
		{"acp_delegate", false},
	}
	for _, c := range cases {
		if got := isReadOnlyTool(c.name, extRO); got != c.want {
			t.Errorf("isReadOnlyTool(%q)=%v want %v", c.name, got, c.want)
		}
	}
}

func TestIsPowerTool(t *testing.T) {
	for name, want := range map[string]bool{
		"write": true, "edit": true, "bash": true, "BASH": true,
		"read": false, "grep": false, "mcp_x_y": false,
	} {
		if got := IsPowerTool(name); got != want {
			t.Errorf("IsPowerTool(%q)=%v want %v", name, got, want)
		}
	}
}

func TestOptionsHTTPClientInjected(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	// A custom client with a marker transport proves Options.HTTPClient is used.
	custom := &http.Client{Transport: &markerTransport{base: srv.Client().Transport}}
	eng, err := New(Options{
		NoSession:   true,
		BaseURL:     srv.URL + "/v1",
		Model:       "m",
		HTTPClient:  custom,
		ConfigPaths: nil,
	})
	if err != nil {
		// api key required — set via env in the test harness
		t.Skipf("engine build: %v", err)
	}
	_, err = eng.Prompt(t.Context(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if hits == 0 || !custom.Transport.(*markerTransport).used {
		t.Fatalf("injected HTTP client not used (hits=%d used=%v)", hits, custom.Transport.(*markerTransport).used)
	}
}

type markerTransport struct {
	base http.RoundTripper
	used bool
}

func (m *markerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	m.used = true
	base := m.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func TestOptionsLoggerInjected(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	eng, err := New(Options{
		NoSession: true,
		Logger:    logger,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(t.Context(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "mow run start") {
		t.Fatalf("injected logger did not capture engine logs: %q", buf.String())
	}
}
