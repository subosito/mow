package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeOAuthMCP is an MCP server that follows the 2025-06-18 authorization
// spec: 401 with resource_metadata, RFC 9728 + RFC 8414 metadata, dynamic
// registration, PKCE-checked auth code, refresh, and resource binding.
type fakeOAuthMCP struct {
	srv         *httptest.Server
	mu          sync.Mutex
	challenge   string // PKCE challenge from /authorize
	resource    string // resource param seen at /authorize
	registered  atomic.Int32
	tokens      atomic.Int32
	refreshes   atomic.Int32
	validToken  string
	lastRefresh string
}

func newFakeOAuthMCP(t *testing.T) *fakeOAuthMCP {
	t.Helper()
	f := &fakeOAuthMCP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              f.srv.URL + "/mcp",
			"authorization_servers": []string{f.srv.URL + "/as"},
			"scopes_supported":      []string{"read:jira"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/as", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           f.srv.URL + "/as",
			"authorization_endpoint":           f.srv.URL + "/as/authorize",
			"token_endpoint":                   f.srv.URL + "/as/token",
			"registration_endpoint":            f.srv.URL + "/as/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("POST /as/register", func(w http.ResponseWriter, r *http.Request) {
		f.registered.Add(1)
		var in struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if len(in.RedirectURIs) != 1 || !strings.HasPrefix(in.RedirectURIs[0], "http://127.0.0.1:") {
			http.Error(w, "bad redirect", 400)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dyn-client"})
	})
	mux.HandleFunc("GET /as/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "dyn-client" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad authorize", 400)
			return
		}
		f.mu.Lock()
		f.challenge = q.Get("code_challenge")
		f.resource = q.Get("resource")
		f.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", "the-code")
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /as/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			f.mu.Lock()
			ok := base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge
			f.mu.Unlock()
			if !ok || r.PostForm.Get("code") != "the-code" || r.PostForm.Get("resource") != f.srv.URL+"/mcp" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
		case "refresh_token":
			f.refreshes.Add(1)
			f.mu.Lock()
			f.lastRefresh = r.PostForm.Get("refresh_token")
			f.mu.Unlock()
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, 400)
			return
		}
		n := f.tokens.Add(1)
		tok := "access-" + string(rune('0'+n))
		f.mu.Lock()
		f.validToken = tok
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": tok, "refresh_token": "refresh-" + string(rune('0'+n)), "expires_in": 3600,
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		valid := f.validToken
		f.mu.Unlock()
		if valid == "" || r.Header.Get("Authorization") != "Bearer "+valid {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []any{map[string]any{"name": "search", "description": "find issues", "inputSchema": map[string]any{"type": "object"}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// followAuthorize plays the browser: GET the authorize URL and follow its
// redirect to mow's loopback callback.
func followAuthorize(t *testing.T) func(string) {
	return func(u string) {
		go func() {
			res, err := http.Get(u)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			res.Body.Close()
		}()
	}
}

func oauthServer(f *fakeOAuthMCP) ServerConfig {
	return ServerConfig{Name: "atlassian", URL: f.srv.URL + "/mcp", Auth: AuthConfig{Type: "oauth"}}
}

func TestOAuthLoginDiscoversRegistersAndSavesGrant(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	f := newFakeOAuthMCP(t)
	if err := oauthLogin(t.Context(), oauthServer(f), followAuthorize(t)); err != nil {
		t.Fatal(err)
	}
	if f.registered.Load() != 1 {
		t.Fatalf("dynamic registrations=%d want 1", f.registered.Load())
	}
	f.mu.Lock()
	resource := f.resource
	f.mu.Unlock()
	if resource != f.srv.URL+"/mcp" {
		t.Fatalf("authorize resource=%q (RFC 8707 binding)", resource)
	}
	st, err := loadOAuthStore("atlassian")
	if err != nil {
		t.Fatal(err)
	}
	if st.AccessToken != "access-1" || st.RefreshToken != "refresh-1" || st.ClientID != "dyn-client" {
		t.Fatalf("saved=%+v", st)
	}
	info, err := os.Stat(oauthStorePath("atlassian"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode=%v err=%v, want 0600", info.Mode().Perm(), err)
	}

	// An engine then uses the saved token without any browser.
	tr, err := newHTTPTransport(oauthServer(f))
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	tools, err := tr.listTools(t.Context())
	if err != nil || len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
}

func TestOAuthRefreshesExpiredGrantAndPersistsRotation(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	f := newFakeOAuthMCP(t)
	if err := oauthLogin(t.Context(), oauthServer(f), followAuthorize(t)); err != nil {
		t.Fatal(err)
	}
	st, _ := loadOAuthStore("atlassian")
	st.Expiry = st.Expiry.AddDate(0, 0, -1) // long expired
	if err := saveOAuthStore("atlassian", st); err != nil {
		t.Fatal(err)
	}
	tr, _ := newHTTPTransport(oauthServer(f))
	if err := tr.initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.lastRefresh
	f.mu.Unlock()
	if f.refreshes.Load() != 1 || last != "refresh-1" {
		t.Fatalf("refreshes=%d with %q", f.refreshes.Load(), last)
	}
	st, _ = loadOAuthStore("atlassian")
	if st.AccessToken != "access-2" || st.RefreshToken != "refresh-2" {
		t.Fatalf("rotated grant not saved: %+v", st)
	}
}

func TestOAuthServerWithoutLoginIsSkippedNotFatal(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	f := newFakeOAuthMCP(t)
	tr, _ := newHTTPTransport(oauthServer(f))
	err := tr.initialize(t.Context())
	if !errors.Is(err, errNotLoggedIn) || !strings.Contains(err.Error(), "mow mcp login atlassian") {
		t.Fatalf("err=%v want a not-logged-in hint", err)
	}
	if err := RegisterServers([]ServerConfig{oauthServer(f)}); err != nil {
		t.Fatalf("a pending login must not fail setup: %v", err)
	}
}

func TestOAuthGrantIsPinnedToItsServerURL(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	f := newFakeOAuthMCP(t)
	if err := oauthLogin(t.Context(), oauthServer(f), followAuthorize(t)); err != nil {
		t.Fatal(err)
	}
	other := oauthServer(f)
	other.URL = f.srv.URL + "/elsewhere"
	tr, _ := newHTTPTransport(other)
	if err := tr.initialize(t.Context()); !errors.Is(err, errNotLoggedIn) {
		t.Fatalf("err=%v: a token saved for one url must not be sent to another", err)
	}
}

func TestOAuthDiscoveryRejectsForeignResourceMetadata(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/prm"`)
		http.Error(w, "unauthorized", 401)
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              "https://attacker.example/mcp",
			"authorization_servers": []string{base + "/as"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL
	_, err := discoverOAuth(t.Context(), srv.Client(), srv.URL+"/mcp", false)
	if err == nil || !strings.Contains(err.Error(), "not this server") {
		t.Fatalf("err=%v want resource mismatch", err)
	}
}

func TestAuthParam(t *testing.T) {
	h := `Bearer realm="x", error="invalid_token", resource_metadata="https://a/b", scope=read`
	if got := authParam(h, "resource_metadata"); got != "https://a/b" {
		t.Fatalf("resource_metadata=%q", got)
	}
	if got := authParam(h, "scope"); got != "read" {
		t.Fatalf("scope=%q", got)
	}
	if got := authParam(h, "missing"); got != "" {
		t.Fatalf("missing=%q", got)
	}
}
