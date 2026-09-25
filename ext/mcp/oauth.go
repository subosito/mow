package mcp

// MCP authorization (spec 2025-06-18): protected resource metadata (RFC 9728),
// authorization server metadata (RFC 8414 / OIDC discovery), dynamic client
// registration (RFC 7591), and authorization code + PKCE with resource
// indicators (RFC 8707).
//
// `mow mcp login <server>` runs the interactive browser flow once and stores
// the grant under $MOW_HOME/mcp-auth. Engines only ever use and refresh that
// grant: a turn never opens a browser (an ACP host such as a TUI could not
// show it), and a server nobody has logged in to is skipped, not fatal.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subosito/mow"
)

// errNotLoggedIn marks a server that needs `mow mcp login`. RegisterServers
// skips such a server with a warning instead of failing engine setup.
var errNotLoggedIn = errors.New("not logged in")

func notLoggedIn(server, why string) error {
	return fmt.Errorf("mcp %s: %w (%s) — run `mow mcp login %s`", server, errNotLoggedIn, why, server)
}

// oauthStore is one saved grant. Server pins it to the MCP URL it was issued
// for: a config that later points the same name at another URL must not
// receive this token.
type oauthStore struct {
	Server       string    `json:"server"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	TokenURL     string    `json:"token_url"`
	Resource     string    `json:"resource"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

func oauthStorePath(server string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(server)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.Trim(b.String(), ".")
	if name == "" {
		name = "mcp"
	}
	return filepath.Join(mow.Home(), "mcp-auth", name+".json")
}

func loadOAuthStore(server string) (*oauthStore, error) {
	raw, err := os.ReadFile(oauthStorePath(server))
	if err != nil {
		return nil, err
	}
	var s oauthStore
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("mcp auth: %s: %w", oauthStorePath(server), err)
	}
	return &s, nil
}

func hasOAuthStore(server string) bool {
	_, err := os.Stat(oauthStorePath(server))
	return err == nil
}

// saveOAuthStore writes the grant 0600 in a 0700 dir, via rename so a crash
// never leaves a half-written token file.
func saveOAuthStore(server string, s *oauthStore) error {
	path := oauthStorePath(server)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func normalizeServerURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

// oauthAccessToken returns a live token from the saved grant, refreshing it
// when it is near expiry. Caller must not hold t.mu.
func (t *tokenSource) oauthAccessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.oauth == nil {
		s, err := loadOAuthStore(t.serverName)
		if err != nil {
			if os.IsNotExist(err) {
				return "", notLoggedIn(t.serverName, "no saved login")
			}
			return "", err
		}
		if normalizeServerURL(s.Server) != normalizeServerURL(t.serverURL) {
			return "", notLoggedIn(t.serverName, "saved login is for a different url")
		}
		t.oauth = s
	}
	s := t.oauth
	if s.AccessToken != "" && time.Now().Before(s.Expiry.Add(-30*time.Second)) {
		return s.AccessToken, nil
	}
	if s.RefreshToken == "" || s.TokenURL == "" {
		return "", notLoggedIn(t.serverName, "login expired")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", s.RefreshToken)
	form.Set("client_id", s.ClientID)
	if s.ClientSecret != "" {
		form.Set("client_secret", s.ClientSecret)
	}
	if s.Resource != "" {
		form.Set("resource", s.Resource)
	}
	tok, err := postTokenForm(ctx, t.client, s.TokenURL, form)
	if err != nil {
		// invalid_grant (revoked / rotated elsewhere): only a new login fixes it.
		return "", notLoggedIn(t.serverName, "refresh failed: "+err.Error())
	}
	s.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		s.RefreshToken = tok.RefreshToken
	}
	s.Expiry = tokenExpiry(tok.ExpiresIn)
	if err := saveOAuthStore(t.serverName, s); err != nil {
		return "", fmt.Errorf("mcp auth: save refreshed login: %w", err)
	}
	return s.AccessToken, nil
}

func tokenExpiry(expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second)
}

func postTokenForm(ctx context.Context, client *http.Client, tokenURL string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token endpoint %d: %s", res.StatusCode, safeOAuthErrorBody(body))
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("token endpoint: invalid json")
	}
	if tok.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("token endpoint: empty access_token")
	}
	return tok, nil
}

// oauthEndpoints is what discovery found for one MCP server.
type oauthEndpoints struct {
	Authorize string
	Token     string
	Register  string
	Resource  string
	Scope     string
}

// authParam reads one auth-param (key="value" or key=token) from a
// WWW-Authenticate challenge.
func authParam(header, key string) string {
	lower := strings.ToLower(header)
	i := strings.Index(lower, strings.ToLower(key)+"=")
	for i > 0 && lower[i-1] != ' ' && lower[i-1] != ',' {
		next := strings.Index(lower[i+1:], strings.ToLower(key)+"=")
		if next < 0 {
			return ""
		}
		i += next + 1
	}
	if i < 0 {
		return ""
	}
	v := header[i+len(key)+1:]
	if strings.HasPrefix(v, `"`) {
		v = v[1:]
		if j := strings.IndexByte(v, '"'); j >= 0 {
			return v[:j]
		}
		return v
	}
	if j := strings.IndexAny(v, ", "); j >= 0 {
		return v[:j]
	}
	return v
}

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil &&
		strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

func getJSON(ctx context.Context, client *http.Client, u string, dst any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst) == nil
}

// wellKnownURLs lists the RFC 8414 / RFC 9728 locations for base under name,
// path-aware first (/.well-known/<name>/<path>), then the origin root.
func wellKnownURLs(base, name string) []string {
	u, err := url.Parse(base)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimRight(u.Path, "/")
	var out []string
	if path != "" {
		out = append(out, origin+"/.well-known/"+name+path)
	}
	return append(out, origin+"/.well-known/"+name)
}

// discoverOAuth finds the authorization endpoints for an MCP server. It sends
// an unauthenticated initialize to read the 401 challenge, then follows
// protected resource metadata to the authorization server. Servers from the
// 2025-03-26 spec without resource metadata fall back to their own origin.
func discoverOAuth(ctx context.Context, client *http.Client, serverURL string, insecure bool) (oauthEndpoints, error) {
	var ep oauthEndpoints
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mow"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return ep, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := client.Do(req)
	if err != nil {
		return ep, err
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20)) //nolint:errcheck
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		return ep, fmt.Errorf("server answered %d without login; it does not ask for OAuth", res.StatusCode)
	}
	challenge := res.Header.Get("WWW-Authenticate")
	ep.Scope = authParam(challenge, "scope")

	prmURLs := wellKnownURLs(serverURL, "oauth-protected-resource")
	if meta := authParam(challenge, "resource_metadata"); meta != "" {
		prmURLs = append([]string{meta}, prmURLs...)
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	issuer := ""
	for _, u := range prmURLs {
		if checkURLScheme(u, insecure) != nil {
			continue
		}
		if getJSON(ctx, client, u, &prm) && len(prm.AuthorizationServers) > 0 {
			issuer = prm.AuthorizationServers[0]
			break
		}
	}
	legacy := issuer == ""
	if legacy {
		u, _ := url.Parse(serverURL)
		issuer = u.Scheme + "://" + u.Host
	} else {
		// RFC 9728 §3.3: the metadata must describe this server, or a
		// hostile document could aim our token at another resource.
		if prm.Resource != "" && !sameOrigin(prm.Resource, serverURL) {
			return ep, fmt.Errorf("resource metadata names %q, not this server", prm.Resource)
		}
		ep.Resource = prm.Resource
		if ep.Scope == "" && len(prm.ScopesSupported) > 0 {
			ep.Scope = strings.Join(prm.ScopesSupported, " ")
		}
	}
	if ep.Resource == "" {
		ep.Resource = normalizeServerURL(serverURL)
	}
	if err := checkURLScheme(issuer, insecure); err != nil {
		return ep, fmt.Errorf("authorization server: %w", err)
	}

	var asm struct {
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	found := false
	candidates := append(wellKnownURLs(issuer, "oauth-authorization-server"), wellKnownURLs(issuer, "openid-configuration")...)
	if u, err := url.Parse(issuer); err == nil && strings.Trim(u.Path, "/") != "" {
		candidates = append(candidates, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration")
	}
	for _, u := range candidates {
		if getJSON(ctx, client, u, &asm) && asm.AuthorizationEndpoint != "" && asm.TokenEndpoint != "" {
			found = true
			break
		}
	}
	switch {
	case found:
		ep.Authorize, ep.Token, ep.Register = asm.AuthorizationEndpoint, asm.TokenEndpoint, asm.RegistrationEndpoint
		if len(asm.CodeChallengeMethods) > 0 && !containsFold(asm.CodeChallengeMethods, "S256") {
			return ep, fmt.Errorf("authorization server does not support PKCE S256")
		}
	case legacy:
		// 2025-03-26 default endpoints on the server origin.
		ep.Authorize, ep.Token, ep.Register = issuer+"/authorize", issuer+"/token", issuer+"/register"
	default:
		return ep, fmt.Errorf("no authorization server metadata at %s", issuer)
	}
	for _, u := range []string{ep.Authorize, ep.Token, ep.Register} {
		if u == "" {
			continue
		}
		if err := checkURLScheme(u, insecure); err != nil {
			return ep, fmt.Errorf("authorization endpoint: %w", err)
		}
	}
	return ep, nil
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// registerClient performs RFC 7591 dynamic registration for a public client
// with a loopback redirect.
func registerClient(ctx context.Context, client *http.Client, regURL, redirect string) (id, secret string, err error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "mow",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, regURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", "", fmt.Errorf("client registration %d: %s", res.StatusCode, safeOAuthErrorBody(raw))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" {
		return "", "", fmt.Errorf("client registration: no client_id")
	}
	return out.ClientID, out.ClientSecret, nil
}

// oauthLogin runs the full interactive flow for one server and saves the
// grant. showURL receives the authorization URL (print it; tests follow it).
func oauthLogin(ctx context.Context, s ServerConfig, showURL func(string)) error {
	serverURL := normalizeServerURL(s.URL)
	if serverURL == "" {
		return fmt.Errorf("mcp %s: login needs a url server", s.Name)
	}
	if err := checkURLScheme(serverURL, s.Insecure); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: checkRedirect(s.Insecure)}
	ep, err := discoverOAuth(ctx, client, serverURL, s.Insecure)
	if err != nil {
		return fmt.Errorf("mcp %s: discover: %w", s.Name, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("mcp auth: listen: %w", err)
	}
	defer ln.Close()
	redirect := "http://" + ln.Addr().String() + "/callback"

	clientID, clientSecret := strings.TrimSpace(s.Auth.ClientID), s.Auth.ClientSecret
	if clientID == "" {
		if ep.Register == "" {
			return fmt.Errorf("mcp %s: server has no client registration; set auth.client_id", s.Name)
		}
		clientID, clientSecret, err = registerClient(ctx, client, ep.Register, redirect)
		if err != nil {
			return fmt.Errorf("mcp %s: %w", s.Name, err)
		}
	}
	state, err := randomHex(16)
	if err != nil {
		return err
	}
	verifier, challenge, err := pkceVerifier()
	if err != nil {
		return err
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	fail := func(err error) {
		select {
		case errCh <- err:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			fail(fmt.Errorf("mcp auth: state mismatch"))
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			fail(fmt.Errorf("mcp auth: %s", e))
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			fail(fmt.Errorf("mcp auth: missing code"))
			return
		}
		_, _ = io.WriteString(w, "mow: login complete — you can close this tab.")
		select {
		case codeCh <- code:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	scope := strings.TrimSpace(s.Auth.Scope)
	if scope == "" {
		scope = ep.Scope
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", ep.Resource)
	if scope != "" {
		q.Set("scope", scope)
	}
	sep := "?"
	if strings.Contains(ep.Authorize, "?") {
		sep = "&"
	}
	showURL(ep.Authorize + sep + q.Encode())

	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case code = <-codeCh:
	case <-time.After(10 * time.Minute):
		return fmt.Errorf("mcp auth: login timed out")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", ep.Resource)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	tok, err := postTokenForm(ctx, client, ep.Token, form)
	if err != nil {
		return fmt.Errorf("mcp %s: %w", s.Name, err)
	}
	return saveOAuthStore(s.Name, &oauthStore{
		Server:       serverURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     ep.Token,
		Resource:     ep.Resource,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tokenExpiry(tok.ExpiresIn),
	})
}
