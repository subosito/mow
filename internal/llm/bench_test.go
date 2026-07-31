package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Baseline perf harness for the SSE stream assembler. The content/tool-arg
// accumulation loop is the hot path of every streamed reply — these benches
// make O(n²) regressions visible (run before and after perf work).

func benchStreamServer(b *testing.B, body string) *Client {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	b.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
}

func benchStreamBody(b *testing.B, chunks int, chunk string) string {
	b.Helper()
	var sb strings.Builder
	for i := 0; i < chunks; i++ {
		fmt.Fprintf(&sb, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk)
	}
	sb.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

// BenchmarkStreamContentAssembly: ~64KiB of reply text in 2k deltas.
func BenchmarkStreamContentAssembly(b *testing.B) {
	body := benchStreamBody(b, 2000, "0123456789abcdef0123456789abcdef") // 32B × 2000 = 64KiB
	c := benchStreamServer(b, body)
	msgs := []Message{{Role: "user", Content: "hi"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg, err := c.ChatStreamHooks(context.Background(), msgs, nil, StreamHooks{})
		if err != nil {
			b.Fatal(err)
		}
		if len(msg.Content) != 64000 {
			b.Fatalf("content len=%d want 64000", len(msg.Content))
		}
	}
}

// BenchmarkStreamToolCallAssembly: 50 tool calls × 40 argument deltas each.
func BenchmarkStreamToolCallAssembly(b *testing.B) {
	var sb strings.Builder
	const calls, perCall = 50, 40
	for i := 0; i < calls*perCall; i++ {
		idx := i % calls
		fmt.Fprintf(&sb, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"call_%d\",\"function\":{\"name\":\"f%d\",\"arguments\":\"{\\\"k\\\":\\\"0123456789abcdef\\\"}\"}}]}}]}\n\n", idx, i, idx)
	}
	sb.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	sb.WriteString("data: [DONE]\n\n")
	c := benchStreamServer(b, sb.String())
	msgs := []Message{{Role: "user", Content: "x"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg, err := c.ChatStreamHooks(context.Background(), msgs, nil, StreamHooks{})
		if err != nil {
			b.Fatal(err)
		}
		if len(msg.ToolCalls) != calls {
			b.Fatalf("tool calls=%d want %d", len(msg.ToolCalls), calls)
		}
	}
}
