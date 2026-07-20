package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// AuthConfig configures remote MCP authentication.
//
//	auth:
//	  type: bearer
//	  token: "…"
//	# or:
//	  type: oauth2_client_credentials
//	  token_url: https://…/oauth/token
//	  client_id: …
//	  client_secret: …
//	  scope: "mcp"
//	# or (CLI interactive; RFC 8628):
//	  type: oauth2_device_code
//	  device_auth_url: https://…/device/code
//	  token_url: https://…/token
//	  client_id: …
//	  scope: "mcp"
//	# or (browser; local callback):
//	  type: oauth2_auth_code
//	  authorize_url: https://…/authorize
//	  token_url: https://…/token
//	  client_id: …
//	  client_secret: …   # optional for public clients
//	  scope: "mcp"
//	  # redirect_uri default http://127.0.0.1:<ephemeral>/callback
//	  # optional static headers still applied from ServerConfig.Headers
type AuthConfig struct {
	Type          string `yaml:"type"` // bearer | oauth2_client_credentials | oauth2_device_code | oauth2_auth_code
	Token         string `yaml:"token"`
	TokenURL      string `yaml:"token_url"`
	DeviceAuthURL string `yaml:"device_auth_url"` // device authorization endpoint
	AuthorizeURL  string `yaml:"authorize_url"`   // auth-code authorization endpoint
	RedirectURI   string `yaml:"redirect_uri"`    // default: ephemeral loopback
	ClientID      string `yaml:"client_id"`
	ClientSecret  string `yaml:"client_secret"`
	Scope         string `yaml:"scope"`
	// Header is the HTTP header name (default Authorization).
	Header string `yaml:"header"`
	// Prefix before the token (default "Bearer ").
	Prefix string `yaml:"prefix"`
}

// tokenSource returns Authorization (or custom) header values, refreshing as needed.
type tokenSource struct {
	cfg    AuthConfig
	mu     sync.Mutex
	token  string
	expiry time.Time
	client *http.Client
}

func newTokenSource(cfg AuthConfig) *tokenSource {
	if cfg.Header == "" {
		cfg.Header = "Authorization"
	}
	typ := strings.ToLower(strings.TrimSpace(cfg.Type))
	if cfg.Prefix == "" && (typ == "bearer" ||
		typ == "oauth2_client_credentials" || typ == "client_credentials" ||
		typ == "oauth2_device_code" || typ == "device_code" ||
		typ == "oauth2_auth_code" || typ == "auth_code" || typ == "authorization_code") {
		cfg.Prefix = "Bearer "
	}
	return &tokenSource{
		cfg:    cfg,
		token:  strings.TrimSpace(cfg.Token),
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *tokenSource) apply(req *http.Request) error {
	if t == nil {
		return nil
	}
	typ := strings.ToLower(strings.TrimSpace(t.cfg.Type))
	switch typ {
	case "", "none":
		return nil
	case "bearer":
		if t.token == "" {
			return fmt.Errorf("mcp auth: empty bearer token")
		}
		req.Header.Set(t.cfg.Header, t.cfg.Prefix+t.token)
		return nil
	case "oauth2_client_credentials", "client_credentials":
		tok, err := t.accessToken(req.Context())
		if err != nil {
			return err
		}
		req.Header.Set(t.cfg.Header, t.cfg.Prefix+tok)
		return nil
	case "oauth2_device_code", "device_code":
		tok, err := t.deviceAccessToken(req.Context())
		if err != nil {
			return err
		}
		req.Header.Set(t.cfg.Header, t.cfg.Prefix+tok)
		return nil
	case "oauth2_auth_code", "auth_code", "authorization_code":
		tok, err := t.authCodeAccessToken(req.Context())
		if err != nil {
			return err
		}
		req.Header.Set(t.cfg.Header, t.cfg.Prefix+tok)
		return nil
	default:
		return fmt.Errorf("mcp auth: unknown type %q (want bearer, oauth2_client_credentials, oauth2_device_code, oauth2_auth_code)", t.cfg.Type)
	}
}

func (t *tokenSource) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Refresh 30s early.
	if t.token != "" && time.Now().Before(t.expiry.Add(-30*time.Second)) {
		return t.token, nil
	}
	if strings.TrimSpace(t.cfg.TokenURL) == "" || strings.TrimSpace(t.cfg.ClientID) == "" {
		return "", fmt.Errorf("mcp auth: token_url and client_id required")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", t.cfg.ClientID)
	form.Set("client_secret", t.cfg.ClientSecret)
	if t.cfg.Scope != "" {
		form.Set("scope", t.cfg.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("mcp auth: token endpoint %d: %s", res.StatusCode, string(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("mcp auth: empty access_token")
	}
	t.token = tok.AccessToken
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	t.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return t.token, nil
}

// deviceAccessToken implements RFC 8628 device authorization grant (CLI-friendly).
// Prints verification_uri + user_code to stderr once per login, then polls token_url.
func (t *tokenSource) deviceAccessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expiry.Add(-30*time.Second)) {
		return t.token, nil
	}
	deviceURL := strings.TrimSpace(t.cfg.DeviceAuthURL)
	tokenURL := strings.TrimSpace(t.cfg.TokenURL)
	clientID := strings.TrimSpace(t.cfg.ClientID)
	if deviceURL == "" || tokenURL == "" || clientID == "" {
		return "", fmt.Errorf("mcp auth: device_auth_url, token_url, and client_id required for oauth2_device_code")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	if t.cfg.Scope != "" {
		form.Set("scope", t.cfg.Scope)
	}
	if t.cfg.ClientSecret != "" {
		form.Set("client_secret", t.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("mcp auth: device endpoint %d: %s", res.StatusCode, string(body))
	}
	var dev struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &dev); err != nil {
		return "", err
	}
	if dev.DeviceCode == "" || dev.UserCode == "" {
		return "", fmt.Errorf("mcp auth: device response missing device_code/user_code")
	}
	interval := time.Duration(dev.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second // RFC suggests 5s default when omitted/too small
	}
	uri := dev.VerificationURI
	if dev.VerificationURIComplete != "" {
		uri = dev.VerificationURIComplete
	}
	fmt.Fprintf(os.Stderr, "mow mcp: complete device login\n  code: %s\n  url:  %s\n", dev.UserCode, uri)

	deadline := time.Now().Add(15 * time.Minute)
	if dev.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second)
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("mcp auth: device login expired")
		}
		tokForm := url.Values{}
		tokForm.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		tokForm.Set("device_code", dev.DeviceCode)
		tokForm.Set("client_id", clientID)
		if t.cfg.ClientSecret != "" {
			tokForm.Set("client_secret", t.cfg.ClientSecret)
		}
		treq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(tokForm.Encode()))
		if err != nil {
			return "", err
		}
		treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tres, err := t.client.Do(treq)
		if err != nil {
			return "", err
		}
		tbody, _ := io.ReadAll(io.LimitReader(tres.Body, 1<<20))
		tres.Body.Close()

		var tok struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			Error       string `json:"error"`
		}
		_ = json.Unmarshal(tbody, &tok)
		if tres.StatusCode >= 200 && tres.StatusCode < 300 && tok.AccessToken != "" {
			t.token = tok.AccessToken
			if tok.ExpiresIn <= 0 {
				tok.ExpiresIn = 3600
			}
			t.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
			return t.token, nil
		}
		switch tok.Error {
		case "authorization_pending", "slow_down":
			if tok.Error == "slow_down" {
				interval += 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(interval):
			}
			continue
		case "expired_token", "access_denied":
			return "", fmt.Errorf("mcp auth: device login %s", tok.Error)
		default:
			if tres.StatusCode == http.StatusBadRequest && tok.Error != "" {
				return "", fmt.Errorf("mcp auth: device token %s", tok.Error)
			}
			return "", fmt.Errorf("mcp auth: token endpoint %d: %s", tres.StatusCode, string(tbody))
		}
	}
}

// authCodeAccessToken runs OAuth2 authorization code with a loopback HTTP callback.
// Prints the authorize URL to stderr; user completes login in a browser.
func (t *tokenSource) authCodeAccessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expiry.Add(-30*time.Second)) {
		return t.token, nil
	}
	authURL := strings.TrimSpace(t.cfg.AuthorizeURL)
	tokenURL := strings.TrimSpace(t.cfg.TokenURL)
	clientID := strings.TrimSpace(t.cfg.ClientID)
	if authURL == "" || tokenURL == "" || clientID == "" {
		return "", fmt.Errorf("mcp auth: authorize_url, token_url, and client_id required for oauth2_auth_code")
	}

	// Test/automation: inject code without browser (redirect_uri must match token exchange).
	if code := strings.TrimSpace(os.Getenv("MOW_MCP_AUTH_CODE")); code != "" {
		redirect := strings.TrimSpace(t.cfg.RedirectURI)
		if redirect == "" {
			redirect = "http://127.0.0.1/callback"
		}
		return t.exchangeAuthCode(ctx, code, redirect, clientID, tokenURL)
	}

	state := randomHex(16)
	redirect := strings.TrimSpace(t.cfg.RedirectURI)
	var ln net.Listener
	var err error
	if redirect == "" {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", fmt.Errorf("mcp auth: listen: %w", err)
		}
		defer ln.Close()
		redirect = "http://" + ln.Addr().String() + "/callback"
	} else {
		return "", fmt.Errorf("mcp auth: leave redirect_uri empty for automatic loopback callback (or set MOW_MCP_AUTH_CODE)")
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("mcp auth: state mismatch")
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			errCh <- fmt.Errorf("mcp auth: %s", e)
			return
		}
		c := r.URL.Query().Get("code")
		if c == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- fmt.Errorf("mcp auth: missing code")
			return
		}
		_, _ = io.WriteString(w, "mow: login complete — you can close this tab.")
		codeCh <- c
	})
	go func() {
		srv := &http.Server{Handler: mux}
		_ = srv.Serve(ln)
	}()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	if t.cfg.Scope != "" {
		q.Set("scope", t.cfg.Scope)
	}
	fullAuth := authURL
	if strings.Contains(authURL, "?") {
		fullAuth += "&" + q.Encode()
	} else {
		fullAuth += "?" + q.Encode()
	}
	fmt.Fprintf(os.Stderr, "mow mcp: open this URL to authorize:\n  %s\n", fullAuth)

	var code string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case code = <-codeCh:
	case <-time.After(10 * time.Minute):
		return "", fmt.Errorf("mcp auth: auth code login timed out")
	}
	return t.exchangeAuthCode(ctx, code, redirect, clientID, tokenURL)
}

func (t *tokenSource) exchangeAuthCode(ctx context.Context, code, redirect, clientID, tokenURL string) (string, error) {

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", clientID)
	if t.cfg.ClientSecret != "" {
		form.Set("client_secret", t.cfg.ClientSecret)
	}
	treq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tres, err := t.client.Do(treq)
	if err != nil {
		return "", err
	}
	defer tres.Body.Close()
	tbody, _ := io.ReadAll(io.LimitReader(tres.Body, 1<<20))
	if tres.StatusCode < 200 || tres.StatusCode >= 300 {
		return "", fmt.Errorf("mcp auth: token endpoint %d: %s", tres.StatusCode, string(tbody))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(tbody, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("mcp auth: empty access_token")
	}
	t.token = tok.AccessToken
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	t.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return t.token, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
