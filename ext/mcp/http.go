package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// checkURLScheme requires https for non-loopback MCP endpoints so bearer and
// OAuth tokens never travel in clear text; insecure: true opts out explicitly.
func checkURLScheme(raw string, insecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mcp url %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if insecure {
			return nil
		}
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("mcp url %q: http to a non-loopback host sends tokens in clear text (use https, or set insecure: true)", raw)
	default:
		return fmt.Errorf("mcp url %q: unsupported scheme %q", raw, u.Scheme)
	}
}

// httpTransport implements MCP Streamable HTTP: POST JSON-RPC to a single URL.
// Accepts application/json or text/event-stream responses.
type httpTransport struct {
	url     string
	headers map[string]string
	auth    *tokenSource
	client  *http.Client
	nextID  atomic.Int64
	session string // optional Mcp-Session-Id from server
}

func newHTTPTransport(s ServerConfig) (*httpTransport, error) {
	u := strings.TrimSpace(s.URL)
	if u == "" {
		return nil, fmt.Errorf("empty url")
	}
	if err := checkURLScheme(u, s.Insecure); err != nil {
		return nil, err
	}
	h := &httpTransport{
		url:     u,
		headers: s.Headers,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
	if typ := strings.TrimSpace(s.Auth.Type); typ != "" && !strings.EqualFold(typ, "none") {
		h.auth = newTokenSource(s.Auth)
	}
	return h, nil
}

func (h *httpTransport) initialize(ctx context.Context) error {
	_, err := h.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mow", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	return h.notify(ctx, "notifications/initialized", map[string]any{})
}

func (h *httpTransport) listTools(ctx context.Context) ([]toolInfo, error) {
	raw, err := h.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []toolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (h *httpTransport) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	raw, err := h.call(ctx, "tools/call", map[string]any{
		"name": name, "arguments": json.RawMessage(args),
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return string(raw), nil
	}
	var b strings.Builder
	for _, block := range res.Content {
		if block.Text != "" {
			b.WriteString(block.Text)
			b.WriteByte('\n')
		}
	}
	out := strings.TrimSpace(b.String())
	if res.IsError {
		return "", fmt.Errorf("mcp tool error: %s", out)
	}
	return out, nil
}

func (h *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := h.nextID.Add(1)
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	if err := h.auth.apply(req); err != nil {
		return nil, err
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		h.session = sid
	}
	// On 401, force token refresh once for oauth2.
	if res.StatusCode == http.StatusUnauthorized && h.auth != nil {
		io.Copy(io.Discard, res.Body) //nolint:errcheck
		h.auth.mu.Lock()
		h.auth.expiry = time.Time{} // force refresh
		h.auth.mu.Unlock()
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range h.headers {
			req2.Header.Set(k, v)
		}
		if h.session != "" {
			req2.Header.Set("Mcp-Session-Id", h.session)
		}
		if err := h.auth.apply(req2); err != nil {
			return nil, err
		}
		res, err = h.client.Do(req2)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("http %d: %s", res.StatusCode, string(b))
	}
	ct := res.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readSSEResult(res.Body, id)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var msg struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("%s", msg.Error.Message)
	}
	return msg.Result, nil
}

func (h *httpTransport) notify(ctx context.Context, method string, params any) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	if err := h.auth.apply(req); err != nil {
		return err
	}
	res, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body) //nolint:errcheck
	return nil
}

func (h *httpTransport) Close() error { return nil }

// readSSEResult reads SSE until a JSON-RPC response with matching id.
func readSSEResult(r io.Reader, wantID int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" && data.Len() > 0 {
			raw := []byte(data.String())
			data.Reset()
			var msg struct {
				ID     any             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if msg.Method != "" && msg.ID == nil {
				continue // notification
			}
			// id may be float64 from JSON
			if !idMatch(msg.ID, wantID) {
				continue
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("%s", msg.Error.Message)
			}
			return msg.Result, nil
		}
	}
	return nil, fmt.Errorf("sse: no response for id %d: %v", wantID, sc.Err())
}

func idMatch(id any, want int64) bool {
	switch v := id.(type) {
	case float64:
		return int64(v) == want
	case int64:
		return v == want
	case int:
		return int64(v) == want
	case json.Number:
		n, _ := v.Int64()
		return n == want
	case string:
		return v == fmt.Sprintf("%d", want)
	default:
		return false
	}
}
