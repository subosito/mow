package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	switch os.Getenv("MCP_HELPER_MODE") {
	case "marker":
		runHelperMarker()
		return
	case "hang_call":
		runHelperHangCall()
		return
	}

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Bytes()
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // notification
		}
		switch req.Method {
		case "initialize":
			res, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"protocolVersion": "2024-11-05"},
			})
			fmt.Println(string(res))
		case "tools/list":
			res, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []any{
						map[string]any{
							"name":        "echo",
							"description": "echoes text",
							"inputSchema": map[string]any{"type": "object"},
						},
					},
				},
			})
			fmt.Println(string(res))
		case "tools/call":
			res, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "hello from helper"}},
					"isError": false,
				},
			})
			fmt.Println(string(res))
		}
	}
}

func runHelperMarker() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Bytes()
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			writeHelperJSON(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05"},
			})
		case "tools/list":
			writeHelperJSON(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []any{}},
			})
			if marker := os.Getenv("MCP_MARKER"); marker != "" {
				_ = os.WriteFile(marker, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
			}
			select {}
		}
	}
}

func runHelperHangCall() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Bytes()
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			writeHelperJSON(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05"},
			})
		case "tools/list":
			writeHelperJSON(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"tools": []any{map[string]any{
						"name": "slow", "description": "hangs",
						"inputSchema": map[string]any{"type": "object"},
					}},
				},
			})
		case "tools/call":
			select {}
		}
	}
}

func writeHelperJSON(v any) {
	raw, _ := json.Marshal(v)
	fmt.Println(string(raw))
}

func TestSafeOAuthErrorBodyExtra(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		contains string
		excludes string
	}{
		{
			name:     "redacts client_secret",
			input:    `{"error": "invalid", "client_secret": "supersecret123"}`,
			contains: "[REDACTED]",
			excludes: "supersecret123",
		},
		{
			name:     "redacts access_token",
			input:    `access_token=secret_token_abc&scope=mcp`,
			contains: "[REDACTED]",
			excludes: "secret_token_abc",
		},
		{
			name:     "truncates long body",
			input:    strings.Repeat("a", 300),
			contains: "…",
		},
		{
			name:     "handles unrelated occurrences",
			input:    `code_format is standard`,
			contains: "code_format is standard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := safeOAuthErrorBody([]byte(tt.input))
			if tt.contains != "" && !strings.Contains(res, tt.contains) {
				t.Errorf("expected %q in %q", tt.contains, res)
			}
			if tt.excludes != "" && strings.Contains(res, tt.excludes) {
				t.Errorf("did not expect %q in %q", tt.excludes, res)
			}
		})
	}
}

func TestCheckURLSchemeExtra(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url      string
		insecure bool
		wantErr  bool
	}{
		{"https://example.com/mcp", false, false},
		{"http://localhost:8080/mcp", false, false},
		{"http://127.0.0.1:8080/mcp", false, false},
		{"http://example.com/mcp", false, true},
		{"http://example.com/mcp", true, false},
		{"ftp://example.com/mcp", false, true},
		{"://invalid", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := checkURLScheme(tt.url, tt.insecure)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkURLScheme(%q, %v) err = %v, wantErr %v", tt.url, tt.insecure, err, tt.wantErr)
			}
		})
	}
}

func TestTokenSourceApplyAndOAuthClientCredentials(t *testing.T) {
	t.Parallel()

	t.Run("unknown auth type", func(t *testing.T) {
		ts := newTokenSource(AuthConfig{Type: "invalid_type"})
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err == nil || !strings.Contains(err.Error(), "unknown type") {
			t.Fatalf("expected unknown type error, got %v", err)
		}
	})

	t.Run("empty bearer token", func(t *testing.T) {
		ts := newTokenSource(AuthConfig{Type: "bearer", Token: ""})
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err == nil || !strings.Contains(err.Error(), "empty bearer token") {
			t.Fatalf("expected empty bearer token error, got %v", err)
		}
	})

	t.Run("client credentials missing fields", func(t *testing.T) {
		ts := newTokenSource(AuthConfig{Type: "oauth2_client_credentials"})
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err == nil || !strings.Contains(err.Error(), "token_url and client_id required") {
			t.Fatalf("expected token_url/client_id error, got %v", err)
		}
	})

	t.Run("client credentials success and refresh", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != http.MethodPost {
				http.Error(w, "bad method", 405)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("token-%d", calls),
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		}))
		defer srv.Close()

		ts := newTokenSource(AuthConfig{
			Type:         "oauth2_client_credentials",
			TokenURL:     srv.URL,
			ClientID:     "my-client",
			ClientSecret: "my-secret",
			Scope:        "mcp",
		})

		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("got header %q, want Bearer token-1", req.Header.Get("Authorization"))
		}

		// Subsequent call should reuse cached token
		req2, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req2.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("got cached header %q, want Bearer token-1", req2.Header.Get("Authorization"))
		}
		if calls != 1 {
			t.Fatalf("expected 1 endpoint call, got %d", calls)
		}
	})
}

func TestDeviceCodeFlow(t *testing.T) {
	t.Parallel()

	t.Run("missing parameters", func(t *testing.T) {
		ts := newTokenSource(AuthConfig{Type: "oauth2_device_code"})
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err == nil || !strings.Contains(err.Error(), "device_auth_url") {
			t.Fatalf("expected missing params error, got %v", err)
		}
	})

	t.Run("success flow with authorization pending", func(t *testing.T) {
		pollCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			sBody := string(body)

			if strings.Contains(r.URL.Path, "/device") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"device_code":               "dev-123",
					"user_code":                 "ABCD-1234",
					"verification_uri":          "https://example.com/verify",
					"verification_uri_complete": "https://example.com/verify?code=ABCD-1234",
					"expires_in":                60,
					"interval":                  1,
				})
				return
			}

			if strings.Contains(r.URL.Path, "/token") {
				pollCount++
				if pollCount == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
					return
				}
				if strings.Contains(sBody, "device_code=dev-123") {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"access_token": "device-token-xyz",
						"expires_in":   3600,
					})
					return
				}
			}
			http.Error(w, "not found", 404)
		}))
		defer srv.Close()

		ts := newTokenSource(AuthConfig{
			Type:          "oauth2_device_code",
			DeviceAuthURL: srv.URL + "/device",
			TokenURL:      srv.URL + "/token",
			ClientID:      "test-cli",
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		tok, err := ts.deviceAccessToken(ctx)
		if err != nil {
			t.Fatalf("deviceAccessToken failed: %v", err)
		}
		if tok != "device-token-xyz" {
			t.Fatalf("got token %q, want device-token-xyz", tok)
		}
	})

	t.Run("expired token error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/device") {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"device_code":      "dev-exp",
					"user_code":        "EXP-1234",
					"verification_uri": "https://example.com/verify",
					"expires_in":       60,
					"interval":         1,
				})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "expired_token"})
		}))
		defer srv.Close()

		ts := newTokenSource(AuthConfig{
			Type:          "oauth2_device_code",
			DeviceAuthURL: srv.URL + "/device",
			TokenURL:      srv.URL + "/token",
			ClientID:      "test-cli",
		})

		_, err := ts.deviceAccessToken(context.Background())
		if err == nil || !strings.Contains(err.Error(), "expired_token") {
			t.Fatalf("expected expired_token error, got %v", err)
		}
	})
}

func TestAuthCodeFlow(t *testing.T) {
	t.Run("missing parameters", func(t *testing.T) {
		ts := newTokenSource(AuthConfig{Type: "oauth2_auth_code"})
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err == nil || !strings.Contains(err.Error(), "authorize_url") {
			t.Fatalf("expected authorize_url error, got %v", err)
		}
	})

	t.Run("MOW_MCP_AUTH_CODE env override", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "env-auth-code-token",
				"expires_in":   3600,
			})
		}))
		defer tokenSrv.Close()

		t.Setenv("MOW_MCP_AUTH_CODE", "test-code-123")

		ts := newTokenSource(AuthConfig{
			Type:         "oauth2_auth_code",
			AuthorizeURL: "https://example.com/auth",
			TokenURL:     tokenSrv.URL,
			ClientID:     "client-id",
		})

		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		if err := ts.apply(req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Header.Get("Authorization") != "Bearer env-auth-code-token" {
			t.Fatalf("got header %q, want Bearer env-auth-code-token", req.Header.Get("Authorization"))
		}
	})

	t.Run("full PKCE loopback server callback", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "code_verifier=") {
				http.Error(w, "missing code_verifier", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "pkce-token-789",
				"expires_in":   3600,
			})
		}))
		defer tokenSrv.Close()

		// authURLFn fires on the apply goroutine below while this goroutine
		// polls for the value, so the capture needs its own lock.
		var (
			authURLMu       sync.Mutex
			authURLCaptured string
		)
		capturedAuthURL := func() string {
			authURLMu.Lock()
			defer authURLMu.Unlock()
			return authURLCaptured
		}
		ts := newTokenSource(AuthConfig{
			Type:         "oauth2_auth_code",
			AuthorizeURL: "https://example.com/auth",
			TokenURL:     tokenSrv.URL,
			ClientID:     "client-pkce",
		})
		ts.authURLFn = func(u string) {
			authURLMu.Lock()
			defer authURLMu.Unlock()
			authURLCaptured = u
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
			done <- ts.apply(req)
		}()

		// Wait for authURL captured
		var authURL string
		for i := 0; i < 50; i++ {
			if authURL = capturedAuthURL(); authURL != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if authURL == "" {
			t.Fatal("authURL was not generated")
		}

		// Simulated browser redirect callback:
		parsedURL, _ := http.NewRequest(http.MethodGet, authURL, nil)
		redirectURI := parsedURL.URL.Query().Get("redirect_uri")
		state := parsedURL.URL.Query().Get("state")

		callbackURL := fmt.Sprintf("%s?state=%s&code=valid-code-123", redirectURI, state)
		resp, err := http.Get(callbackURL)
		if err != nil {
			t.Fatalf("callback request failed: %v", err)
		}
		resp.Body.Close()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("apply failed: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("authCodeAccessToken timed out")
		}
	})
}

func TestHTTPTransport401TokenRefresh(t *testing.T) {
	t.Parallel()

	unauthorizedCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "refreshed-token",
				"expires_in":   3600,
			})
			return
		}

		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			unauthorizedCount++
			http.Error(w, "Unauthorized", 401)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"status": "ok"},
		})
	}))
	defer srv.Close()

	tr, err := newHTTPTransport(ServerConfig{
		URL: srv.URL + "/mcp",
		Auth: AuthConfig{
			Type:         "oauth2_client_credentials",
			TokenURL:     srv.URL + "/token",
			ClientID:     "c1",
			ClientSecret: "s1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Preset expired/invalid token in tokenSource
	tr.auth.token = "stale-token"
	tr.auth.expiry = time.Now().Add(10 * time.Minute)

	raw, err := tr.call(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if !strings.Contains(string(raw), `"status":"ok"`) {
		t.Fatalf("expected ok result after token refresh, got %s", string(raw))
	}
	if unauthorizedCount != 1 {
		t.Fatalf("expected 1 401 response before refresh, got %d", unauthorizedCount)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestHTTPTransportSSEReading(t *testing.T) {
	t.Parallel()

	sseData := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":123,\"result\":{\"greeting\":\"hello\"}}\n\n"
	res, err := readSSEResult(strings.NewReader(sseData), 123)
	if err != nil {
		t.Fatalf("readSSEResult failed: %v", err)
	}
	if !strings.Contains(string(res), `"greeting":"hello"`) {
		t.Fatalf("unexpected SSE result: %s", string(res))
	}

	// Test id matching variations
	if !idMatch(float64(123), 123) || !idMatch(int64(123), 123) || !idMatch("123", 123) || !idMatch(json.Number("123"), 123) {
		t.Fatal("idMatch failed for matching values")
	}
	if idMatch("456", 123) {
		t.Fatal("idMatch matched invalid ID")
	}
}

func TestMCPServerCmdAndLifecycle(t *testing.T) {
	t.Parallel()

	for _, h := range []string{"-h", "--help", "help"} {
		if code := serveCmd([]string{h}); code != 0 {
			t.Errorf("serveCmd(%q) = %d; want 0", h, code)
		}
	}

	if code := serveCmd([]string{"--invalid-flag-xyz"}); code != 2 {
		t.Errorf("serveCmd(invalid flag) = %d; want 2", code)
	}

	if code := serveCmd([]string{"--no-session"}); code != 1 {
		t.Errorf("serveCmd(--no-session) = %d; want 1", code)
	}

	// Server stdio JSON-RPC loop
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "server response"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"mow_prompt","arguments":{"prompt":"hi"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":5,"method":"unknown"}` + "\n" +
			`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"unknown_tool"}}` + "\n" +
			`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"mow_prompt","arguments":{"prompt":""}}}` + "\n",
	)
	var out bytes.Buffer
	code := serve(context.Background(), eng, in, &out)
	if code != 0 {
		t.Fatalf("serve returned code %d, want 0", code)
	}

	s := out.String()
	if !strings.Contains(s, "mow_prompt") || !strings.Contains(s, "server response") || !strings.Contains(s, "method not found") {
		t.Fatalf("unexpected server output: %s", s)
	}
}

func TestConfigResolvedAndFallbackFiles(t *testing.T) {
	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"b_server": {Command: "b"},
			"a_server": {Command: "a"},
		},
		Servers: []ServerConfig{
			{Name: "c_server", Command: "c"},
		},
	}

	res := cfg.resolved()
	if len(res) != 3 {
		t.Fatalf("expected 3 resolved servers, got %d", len(res))
	}
	if res[0].Name != "a_server" || res[1].Name != "b_server" || res[2].Name != "c_server" {
		t.Fatalf("unexpected order: %v, %v, %v", res[0].Name, res[1].Name, res[2].Name)
	}

	// Test fallback config files in registerAll (host path list includes
	// $MOW_HOME/config.yaml so home mcp.json is eligible).
	tmpHome := t.TempDir()
	t.Setenv("MOW_HOME", tmpHome)

	mcpFile := filepath.Join(tmpHome, "mcp.json")
	_ = os.WriteFile(mcpFile, []byte(`{"mcpServers": {}}`), 0600)

	if err := registerAll(filepath.Join(tmpHome, "config.yaml")); err != nil {
		t.Fatalf("registerAll failed with fallback file: %v", err)
	}
}

func TestRegisterServersHTTPAndToolCalling(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"protocolVersion": "2025-03-26"},
			})
		case "notifications/initialized":
			w.WriteHeader(200)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []any{
						map[string]any{
							"name":        "search",
							"description": "search tool",
							"inputSchema": map[string]any{"type": "object"},
							"annotations": map[string]any{"readOnlyHint": true},
						},
					},
				},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []any{
						map[string]any{"type": "text", "text": "search result"},
					},
					"isError": false,
				},
			})
		default:
			http.Error(w, "unknown method", 400)
		}
	}))
	defer srv.Close()

	servers := []ServerConfig{
		{
			Name: "test_http",
			URL:  srv.URL,
		},
	}

	if err := RegisterServers(servers); err != nil {
		t.Fatalf("RegisterServers failed: %v", err)
	}

	// Verify transport methods on httpTransport
	ht, err := newHTTPTransport(ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := ht.initialize(context.Background()); err != nil {
		t.Fatalf("ht.initialize failed: %v", err)
	}
	tools, err := ht.listTools(context.Background())
	if err != nil || len(tools) == 0 {
		t.Fatalf("ht.listTools failed: %v", err)
	}
	res, err := ht.callTool(context.Background(), "search", json.RawMessage(`{}`))
	if err != nil || res != "search result" {
		t.Fatalf("ht.callTool got %q, %v", res, err)
	}
}

func TestStdioServerSubprocess(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Name:    "stdio_test",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	}

	rc := &reconnectingClient{cfg: cfg}
	defer rc.Close()

	ctx := context.Background()
	tools, err := rc.listTools(ctx)
	if err != nil {
		t.Fatalf("rc.listTools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	out, err := rc.callTool(ctx, "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("rc.callTool failed: %v", err)
	}
	if out != "hello from helper" {
		t.Fatalf("got %q, want 'hello from helper'", out)
	}
}

func TestMCPToolProperties(t *testing.T) {
	t.Parallel()

	tool := &mcpTool{
		prefix:   "test",
		name:     "my_tool",
		desc:     "description",
		readOnly: true,
	}

	if !tool.ReadOnly() {
		t.Error("expected ReadOnly true")
	}
	if !tool.Untrusted() {
		t.Error("expected Untrusted true")
	}
	if tool.Name() != "mcp_test_my_tool" {
		t.Errorf("got name %q, want mcp_test_my_tool", tool.Name())
	}
	if !strings.Contains(tool.Description(), "[mcp:test]") {
		t.Errorf("got description %q", tool.Description())
	}
	if string(tool.Parameters()) != `{"type":"object","properties":{}}` {
		t.Errorf("got parameters %s", string(tool.Parameters()))
	}
}
