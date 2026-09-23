package llm

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChatWithStreamRetriesOneIncompleteToolOnlyResponse(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}}]}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client(), Stream: true}
	msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err != nil || msg.Content != "ok" || hits.Load() != 2 {
		t.Fatalf("msg=%+v err=%v hits=%d", msg, err, hits.Load())
	}
}
func TestChatWithStreamDoesNotRetryIncompleteVisibleContent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client(), Stream: true}
	_, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err == nil || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}
