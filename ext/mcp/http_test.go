package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPTransportJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2025-03-26"},
			})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "echo", "description": "e", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "pong"}},
				},
			})
		default:
			http.Error(w, "bad method", 400)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tr, err := newHTTPTransport(ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, err := tr.listTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("%+v %v", tools, err)
	}
	out, err := tr.callTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil || out != "pong" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestCheckURLScheme(t *testing.T) {
	cases := []struct {
		url      string
		insecure bool
		wantErr  bool
	}{
		{"https://mcp.example.com/rpc", false, false},
		{"http://127.0.0.1:8080/rpc", false, false},
		{"http://localhost:8080/rpc", false, false},
		{"http://[::1]:8080/rpc", false, false},
		{"http://mcp.example.com/rpc", false, true},
		{"http://mcp.example.com/rpc", true, false},
		{"ftp://mcp.example.com/rpc", false, true},
	}
	for _, c := range cases {
		err := checkURLScheme(c.url, c.insecure)
		if (err != nil) != c.wantErr {
			t.Errorf("checkURLScheme(%q, insecure=%v) err=%v wantErr=%v", c.url, c.insecure, err, c.wantErr)
		}
	}
}
