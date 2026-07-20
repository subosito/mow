package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuth2DeviceCode(t *testing.T) {
	var deviceHits, tokenHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		deviceHits.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "cid" {
			http.Error(w, "cid", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dc-1",
			"user_code":        "ABCD-EFGH",
			"verification_uri": "https://example.com/device",
			"expires_in":       600,
			"interval":         1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		n := tokenHits.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
			http.Error(w, "grant", 400)
			return
		}
		if n < 2 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "device-tok",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ts := newTokenSource(AuthConfig{
		Type:          "oauth2_device_code",
		DeviceAuthURL: srv.URL + "/device",
		TokenURL:      srv.URL + "/token",
		ClientID:      "cid",
		Scope:         "mcp",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x", nil)
	if err := ts.apply(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer device-tok" {
		t.Fatalf("auth=%q hits device=%d token=%d", got, deviceHits.Load(), tokenHits.Load())
	}
	if deviceHits.Load() != 1 || tokenHits.Load() < 2 {
		t.Fatalf("device=%d token=%d", deviceHits.Load(), tokenHits.Load())
	}
}

func TestOAuth2AuthCode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "abc" {
			http.Error(w, "bad", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "code-tok",
			"expires_in":   3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("MOW_MCP_AUTH_CODE", "abc")
	ts := newTokenSource(AuthConfig{
		Type:         "oauth2_auth_code",
		AuthorizeURL: "http://example.com/authorize",
		TokenURL:     srv.URL + "/token",
		ClientID:     "cid",
		RedirectURI:  "http://127.0.0.1/callback",
	})
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	if err := ts.apply(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer code-tok" {
		t.Fatalf("got %q", got)
	}
}

func TestOAuth2AuthCodePKCE(t *testing.T) {
	var mu sync.Mutex
	var challenge string // code_challenge seen on the authorize URL
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "abc" {
			http.Error(w, "bad", 400)
			return
		}
		v := r.Form.Get("code_verifier")
		if v == "" {
			http.Error(w, "missing code_verifier", 400)
			return
		}
		sum := sha256.Sum256([]byte(v))
		mu.Lock()
		want := challenge
		mu.Unlock()
		if base64.RawURLEncoding.EncodeToString(sum[:]) != want {
			http.Error(w, "code_verifier does not match code_challenge", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "pkce-tok",
			"expires_in":   3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ts := newTokenSource(AuthConfig{
		Type:         "oauth2_auth_code",
		AuthorizeURL: "http://example.com/authorize",
		TokenURL:     srv.URL + "/token",
		ClientID:     "cid",
	})
	ts.authURLFn = func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Errorf("parse authorize url: %v", err)
			return
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" {
			t.Errorf("code_challenge_method=%q want S256", q.Get("code_challenge_method"))
		}
		ch := q.Get("code_challenge")
		if len(ch) != 43 { // base64url(SHA256) without padding
			t.Errorf("code_challenge=%q want 43 chars", ch)
		}
		mu.Lock()
		challenge = ch
		mu.Unlock()
		// Simulate the provider redirecting the browser to the loopback callback.
		res, err := http.Get(q.Get("redirect_uri") + "?code=abc&state=" + url.QueryEscape(q.Get("state")))
		if err != nil {
			t.Errorf("callback: %v", err)
			return
		}
		res.Body.Close()
	}
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	if err := ts.apply(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer pkce-tok" {
		t.Fatalf("got %q", got)
	}
}

func TestOAuth2AuthCodeUniqueStateAndVerifier(t *testing.T) {
	var mu sync.Mutex
	var verifiers []string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		verifiers = append(verifiers, r.Form.Get("code_verifier"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"expires_in":   3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ts := newTokenSource(AuthConfig{
		Type:         "oauth2_auth_code",
		AuthorizeURL: "http://example.com/authorize",
		TokenURL:     srv.URL + "/token",
		ClientID:     "cid",
	})
	var states, challenges []string
	ts.authURLFn = func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Errorf("parse authorize url: %v", err)
			return
		}
		q := u.Query()
		states = append(states, q.Get("state"))
		challenges = append(challenges, q.Get("code_challenge"))
		res, err := http.Get(q.Get("redirect_uri") + "?code=abc&state=" + url.QueryEscape(q.Get("state")))
		if err != nil {
			t.Errorf("callback: %v", err)
			return
		}
		res.Body.Close()
	}
	for i := 0; i < 2; i++ {
		// Force a fresh login each round.
		ts.mu.Lock()
		ts.token = ""
		ts.expiry = time.Time{}
		ts.mu.Unlock()
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		if err := ts.apply(req); err != nil {
			t.Fatal(err)
		}
	}
	if len(states) != 2 || len(challenges) != 2 {
		t.Fatalf("flows=%d want 2", len(states))
	}
	if states[0] == states[1] {
		t.Fatalf("state reused across flows: %q", states[0])
	}
	if challenges[0] == challenges[1] {
		t.Fatalf("code_challenge reused across flows: %q", challenges[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(verifiers) != 2 || verifiers[0] == "" || verifiers[0] == verifiers[1] {
		t.Fatalf("verifiers=%q want 2 unique non-empty", verifiers)
	}
}

func TestBearerApply(t *testing.T) {
	ts := newTokenSource(AuthConfig{Type: "bearer", Token: "sekrit"})
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	if err := ts.apply(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sekrit" {
		t.Fatalf("got %q", got)
	}
}

func TestBearerApplyNil(t *testing.T) {
	var ts *tokenSource
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	if err := ts.apply(req); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("nil auth should not set header")
	}
}

func TestOAuth2ClientCredentials(t *testing.T) {
	var tokenHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenHits.Add(1)
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, "grant", 400)
			return
		}
		if r.Form.Get("client_id") != "cid" || r.Form.Get("client_secret") != "csec" {
			http.Error(w, "creds", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-" + r.Form.Get("scope"),
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-mcp" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"ok": true, "method": req.Method},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tr, err := newHTTPTransport(ServerConfig{
		URL: srv.URL + "/mcp",
		Auth: AuthConfig{
			Type:         "oauth2_client_credentials",
			TokenURL:     srv.URL + "/token",
			ClientID:     "cid",
			ClientSecret: "csec",
			Scope:        "mcp",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tr.call(context.Background(), "initialize", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ok":true`) && !strings.Contains(string(raw), `"ok": true`) {
		t.Fatalf("result=%s", raw)
	}
	// Cached token — second call should not re-hit token endpoint.
	if _, err := tr.call(context.Background(), "ping", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if n := tokenHits.Load(); n != 1 {
		t.Fatalf("token endpoint hits=%d want 1", n)
	}
}

func TestOAuth2RefreshOn401(t *testing.T) {
	var tokenN atomic.Int32
	var mcpN atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		n := tokenN.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "t" + strconv.Itoa(int(n)),
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		mcpN.Add(1)
		auth := r.Header.Get("Authorization")
		// Stale pre-seeded token rejected; refreshed t1 accepted.
		if auth == "Bearer stale" {
			http.Error(w, "expired", 401)
			return
		}
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"auth": auth},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tr, err := newHTTPTransport(ServerConfig{
		URL: srv.URL + "/mcp",
		Auth: AuthConfig{
			Type:         "oauth2_client_credentials",
			TokenURL:     srv.URL + "/token",
			ClientID:     "c",
			ClientSecret: "s",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Pre-seed a cached token the MCP server will reject once.
	tr.auth.mu.Lock()
	tr.auth.token = "stale"
	tr.auth.expiry = time.Now().Add(time.Hour)
	tr.auth.mu.Unlock()

	raw, err := tr.call(context.Background(), "tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Bearer t1") {
		t.Fatalf("want refreshed auth in result, got %s", raw)
	}
	if tokenN.Load() < 1 {
		t.Fatal("expected token refresh after 401")
	}
	if mcpN.Load() < 2 {
		t.Fatalf("expected retry after 401, mcp hits=%d", mcpN.Load())
	}
}

func TestHTTPBearerHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer static-tok" {
			http.Error(w, "nope", 401)
			return
		}
		var req struct {
			ID any `json:"id"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tr, err := newHTTPTransport(ServerConfig{
		URL:  srv.URL,
		Auth: AuthConfig{Type: "bearer", Token: "static-tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.call(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
}
