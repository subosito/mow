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
