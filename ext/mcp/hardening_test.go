package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow/ext"
)

// pipeClient wires a client to an in-process fake server over io.Pipe. The
// server goroutine answers every request with reply(id, method, params) after
// a short delay so concurrent callers interleave on the shared stdout reader.
func pipeClient(t *testing.T, reply func(id json.RawMessage, method string) any) *client {
	t.Helper()
	inR, inW := io.Pipe()   // client stdin → server
	outR, outW := io.Pipe() // server → client stdout
	go func() {
		defer outW.Close()
		sc := bufio.NewScanner(inR)
		var wg sync.WaitGroup
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
				continue
			}
			wg.Go(func() {
				time.Sleep(5 * time.Millisecond)
				raw, _ := json.Marshal(reply(req.ID, req.Method))
				_, _ = outW.Write(append(raw, '\n'))
			})
		}
		wg.Wait()
	}()
	c := &client{stdin: inW, stdout: bufio.NewReader(outR)}
	t.Cleanup(func() { _ = c.Close(); _ = outR.Close() })
	return c
}

func echoIDReply(id json.RawMessage, _ string) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"echo": json.RawMessage(id)}}
}

// Parallel tool batches share one stdio server. Before call() serialized the
// exchange, two callers raced on the bufio.Reader and one consumed the
// other's reply (which it then dropped as !isReplyTo), so the other timed out.
func TestCallConcurrentCallersGetOwnReply(t *testing.T) {
	c := pipeClient(t, echoIDReply)
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			want := c.nextID.Load() // lower bound; the exact id is echoed back
			raw, err := c.call(ctx, "tools/call", map[string]any{})
			if err != nil {
				errs <- err
				return
			}
			var res struct {
				Echo int64 `json:"echo"`
			}
			if err := json.Unmarshal(raw, &res); err != nil || res.Echo <= want {
				errs <- fmt.Errorf("got %s (%v), want an id > %d", raw, err, want)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestReadBoundedLineFailsBeforeBufferingWholeLine(t *testing.T) {
	long := strings.Repeat("x", 100<<10) + "\n"
	r := bufio.NewReader(strings.NewReader(long))
	_, err := readBoundedLine(r, 8<<10)
	if !errors.Is(err, errLineExceeds) {
		t.Fatalf("err=%v want errLineExceeds", err)
	}
	// Only the first fragments were consumed; the rest is still unread.
	if r.Buffered()+len(long)-(100<<10) <= 0 {
		t.Fatalf("whole line was read anyway")
	}
	rest, _ := io.ReadAll(r)
	if len(rest) < 80<<10 {
		t.Fatalf("read %d bytes past the cap before failing", len(long)-len(rest))
	}

	ok := bufio.NewReader(strings.NewReader("short\nnext"))
	line, err := readBoundedLine(ok, 64)
	if err != nil || string(line) != "short\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	line, err = readBoundedLine(ok, 64)
	if err != io.EOF || string(line) != "next" {
		t.Fatalf("tail line=%q err=%v", line, err)
	}
}

func TestIsTransportErr(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrClosedPipe, os.ErrClosed, &exec.ExitError{},
		fmt.Errorf("mcp: stdout %w", fmt.Errorf("%w 1 bytes", errLineExceeds)), fmt.Errorf("write: %w", io.ErrUnexpectedEOF)} {
		if !isTransportErr(err) {
			t.Errorf("%v should be a transport error", err)
		}
	}
	for _, err := range []error{nil, errors.New("mcp tool error: boom"), errors.New("Invalid params"), context.DeadlineExceeded} {
		if isTransportErr(err) {
			t.Errorf("%v should not be a transport error", err)
		}
	}
}

// A tool result with isError or a JSON-RPC error is the server's answer. It
// used to trigger reset+retry, killing server state and re-running the tool.
func TestReconnectingClientKeepsServerOnToolError(t *testing.T) {
	for name, frames := range map[string]string{
		"isError":   `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"nope"}],"isError":true}}` + "\n",
		"rpc error": `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			fake, wrote := fakeStdio(t, frames)
			rc := &reconnectingClient{cfg: helperConfig(t, "keep", ""), c: fake}
			defer rc.Close()
			_, err := rc.callTool(t.Context(), "echo", nil)
			if err == nil {
				t.Fatal("expected the server's error")
			}
			if rc.c != fake {
				t.Fatalf("server was restarted on a tool error: %v", err)
			}
			if n := strings.Count(wrote.String(), `"tools/call"`); n != 1 {
				t.Fatalf("tools/call sent %d times, want 1 (no retry)", n)
			}
		})
	}
}

// A dead pipe is a transport error: reconnect once and retry.
func TestReconnectingClientRetriesOnEOF(t *testing.T) {
	setupMCPTest(t)
	fake, _ := fakeStdio(t, "") // stdout at EOF: the process is gone
	rc := &reconnectingClient{cfg: helperConfig(t, "eof", ""), c: fake}
	defer rc.Close()
	out, err := rc.callTool(t.Context(), "echo", nil)
	if err != nil || out != "hello from helper" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if rc.c == fake || rc.c == nil {
		t.Fatal("expected a fresh client after EOF")
	}
}

func TestReconnectingClientEnsureAfterCloseFails(t *testing.T) {
	setupMCPTest(t)
	rc := &reconnectingClient{cfg: helperConfig(t, "closed", "")}
	if err := rc.ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	pid := rc.processPID()
	_ = rc.Close()
	waitPIDDead(t, pid, "Close left the helper alive")
	if err := rc.ensure(t.Context()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("ensure after Close = %v, want closed error", err)
	}
	if _, err := rc.callTool(t.Context(), "echo", nil); err == nil {
		t.Fatal("callTool resurrected a closed transport")
	}
	if rc.processPID() != 0 {
		t.Fatal("a process is live after Close")
	}
}

func hangAtConfig(t *testing.T, name, method string) ServerConfig {
	t.Helper()
	cfg := helperConfig(t, name, "hang_at")
	cfg.Env["MCP_HANG_AT"] = method
	cfg.TimeoutSec = 1
	return cfg
}

// initialize and tools/list ran with t.Context(): a silent server
// pinned mow.New forever. They now share the tools/call deadline.
func TestHandshakeHonorsCallTimeout(t *testing.T) {
	setupMCPTest(t)
	start := time.Now()
	if _, err := startServer(hangAtConfig(t, "hang-init", "initialize")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startServer err=%v want deadline exceeded", err)
	}
	if err := RegisterServers([]ServerConfig{hangAtConfig(t, "hang-list", "tools/list")}); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RegisterServers err=%v want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("handshake blocked for %v", elapsed)
	}
}

func TestMCPToolNameSanitizesPrefix(t *testing.T) {
	tl := &mcpTool{prefix: "my-server.v2", name: "do-it"}
	if got := tl.Name(); got != "mcp_my_server_v2_do_it" {
		t.Fatalf("Name()=%q", got)
	}
}

// toolListServer is a fake streamable-HTTP MCP server exposing tools.
func toolListServer(t *testing.T, tools []any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2025-03-26"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": tools}})
		default:
			w.WriteHeader(200)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegisterServersCapsMetadataAndSkipsDuplicates(t *testing.T) {
	setupMCPTest(t)
	longDesc := strings.Repeat("d", 5000)
	bigSchema := map[string]any{"type": "object", "description": strings.Repeat("s", maxToolSchemaBytes+1)}
	a := toolListServer(t, []any{
		map[string]any{"name": "t", "description": "first", "inputSchema": map[string]any{"type": "object"}},
		map[string]any{"name": "long", "description": longDesc, "inputSchema": map[string]any{"type": "object"}},
		map[string]any{"name": "big", "description": "too big", "inputSchema": bigSchema},
	})
	b := toolListServer(t, []any{
		map[string]any{"name": "t", "description": "second", "inputSchema": map[string]any{"type": "object"}},
	})
	// "a-b" and "a_b" sanitize to the same prefix: the second "t" must be
	// skipped, not silently replace the first.
	err := RegisterServers([]ServerConfig{{Name: "a-b", URL: a.URL, Insecure: true}, {Name: "a_b", URL: b.URL, Insecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ext.Tool{}
	for _, tl := range ext.Tools() {
		byName[tl.Name()] = tl
	}
	if _, ok := byName["mcp_a_b_big"]; ok {
		t.Fatal("oversized inputSchema was registered")
	}
	tl, ok := byName["mcp_a_b_t"]
	if !ok || !strings.Contains(tl.Description(), "first") {
		t.Fatalf("mcp_a_b_t = %v (want the first server's tool)", tl)
	}
	long, ok := byName["mcp_a_b_long"]
	if !ok || len(long.Description()) > maxToolDescBytes+64 || !strings.HasSuffix(long.Description(), "…") {
		t.Fatalf("description not capped: len=%d", len(long.Description()))
	}
	for name := range byName {
		if strings.Contains(name, "-") {
			t.Fatalf("unsanitized tool name %q", name)
		}
	}
}

// refreshTokenServer answers authorization_code with a short-lived token plus
// refresh "r1", refreshes r1 → tok2/r2, and rejects any other refresh token.
func refreshTokenServer(t *testing.T, grants *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g := r.Form.Get("grant_type")
		mu.Lock()
		*grants = append(*grants, g)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case g == "authorization_code":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok1", "refresh_token": "r1", "expires_in": 1})
		case g == "refresh_token" && r.Form.Get("refresh_token") == "r1" && r.Form.Get("client_id") == "cid":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok2", "refresh_token": "r2", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// On expiry the refresh_token grant runs first; the interactive login only
// re-runs when there is no refresh token or the refresh is rejected.
func TestAuthCodeUsesRefreshTokenBeforeInteractiveLogin(t *testing.T) {
	var (
		grants []string
		mu     sync.Mutex
	)
	srv := refreshTokenServer(t, &grants, &mu)
	t.Setenv("MOW_MCP_AUTH_CODE", "abc")
	ts := newTokenSource(AuthConfig{
		Type: "oauth2_auth_code", AuthorizeURL: "https://example.com/auth",
		TokenURL: srv.URL, ClientID: "cid", RedirectURI: "http://127.0.0.1/callback",
	})
	ts.authURLFn = func(string) { t.Fatal("interactive login must not run") }
	header := func() string {
		req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
		if err := ts.apply(req); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	if got := header(); got != "Bearer tok1" {
		t.Fatalf("first apply = %q", got)
	}
	// expires_in=1 is inside the 30s early-refresh window: the next apply
	// must refresh, not log in again.
	if got := header(); got != "Bearer tok2" {
		t.Fatalf("second apply = %q", got)
	}
	// Forced expiry (what a 401 does) with a refresh token the server now
	// rejects: fall back to the login flow and keep going.
	ts.mu.Lock()
	ts.expiry = time.Time{}
	ts.mu.Unlock()
	if got := header(); got != "Bearer tok1" {
		t.Fatalf("fallback apply = %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"authorization_code", "refresh_token", "refresh_token", "authorization_code"}
	if strings.Join(grants, ",") != strings.Join(want, ",") {
		t.Fatalf("grants = %v want %v", grants, want)
	}
}

func TestDeviceCodeUsesRefreshToken(t *testing.T) {
	var deviceHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		deviceHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "CODE", "verification_uri": "https://example.com/d", "interval": 1})
	})
	var grants []string
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g := r.Form.Get("grant_type")
		grants = append(grants, g)
		if g == "refresh_token" && r.Form.Get("refresh_token") == "rd" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "d2", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "d1", "refresh_token": "rd", "expires_in": 1})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ts := newTokenSource(AuthConfig{Type: "oauth2_device_code", DeviceAuthURL: srv.URL + "/device", TokenURL: srv.URL + "/token", ClientID: "cid"})
	for i, want := range []string{"Bearer d1", "Bearer d2"} {
		req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
		if err := ts.apply(req); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != want {
			t.Fatalf("apply %d = %q want %q", i, got, want)
		}
	}
	if deviceHits != 1 || len(grants) != 2 || grants[1] != "refresh_token" {
		t.Fatalf("device hits=%d grants=%v", deviceHits, grants)
	}
}

// A second bad hit on /callback used to block its handler forever on the full
// errCh, and the callback server was never shut down.
func TestAuthCodeCallbackServerShutsDownAndToleratesRepeatHits(t *testing.T) {
	var (
		mu      sync.Mutex
		authURL string
	)
	ts := newTokenSource(AuthConfig{Type: "oauth2_auth_code", AuthorizeURL: "https://example.com/auth", TokenURL: "https://example.com/token", ClientID: "cid"})
	ts.authURLFn = func(u string) { mu.Lock(); authURL = u; mu.Unlock() }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://x", nil)
		done <- ts.apply(req)
	}()
	var redirect string
	for i := 0; i < 200 && redirect == ""; i++ {
		mu.Lock()
		if authURL != "" {
			u, _ := http.NewRequest(http.MethodGet, authURL, nil)
			redirect = u.URL.Query().Get("redirect_uri")
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if redirect == "" {
		t.Fatal("no authorize URL")
	}
	hc := &http.Client{Timeout: 2 * time.Second}
	for i := range 2 {
		// The second hit either gets served (non-blocking send) or is refused
		// because the first already finished the login; it must never hang.
		res, err := hc.Get(redirect + "?state=wrong&code=x")
		if err != nil && (i == 0 || !strings.Contains(err.Error(), "connection refused")) {
			t.Fatalf("callback hit %d: %v (handler blocked?)", i, err)
		}
		if err == nil {
			res.Body.Close()
		}
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "state mismatch") {
			t.Fatalf("apply err=%v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("apply did not return")
	}
	if _, err := hc.Get(redirect); err == nil {
		t.Fatal("callback server still listening after login finished")
	}
}

// A call queued behind a slow sibling must not spend its per-call timeout
// waiting for the exchange slot: the budget starts once the call owns the
// reader. With a 150ms server and a 100ms timeout, a shared budget would
// fail the second caller; a per-exchange budget lets both succeed.
func TestQueuedCallDoesNotBurnTimeoutWaiting(t *testing.T) {
	c := pipeClient(t, func(id json.RawMessage, m string) any {
		time.Sleep(150 * time.Millisecond)
		return echoIDReply(id, m)
	})
	c.callTO = 250 * time.Millisecond
	const n = 3 // 3 × 150ms serialized = 450ms > any single 250ms budget
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			if _, err := c.call(t.Context(), "tools/call", map[string]any{}); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("queued call failed: %v", err)
	}
}
