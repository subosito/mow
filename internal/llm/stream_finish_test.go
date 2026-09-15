package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamCompletionAllowsHTTPEOF(t *testing.T) {
	frames := map[string]string{
		WireOpenAIChat: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n",
		WireOpenAIResponses: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1},\"output\":[]}}\n\n",
		WireAnthropicMsg: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for wire, frame := range frames {
		for _, http2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/http2=%t", wire, http2), func(t *testing.T) {
				canceled := make(chan bool, 1)
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, frame)
					w.(http.Flusher).Flush()
					// Final bookkeeping after flushing the protocol's terminal frame.
					select {
					case <-r.Context().Done():
						canceled <- true
					case <-time.After(20 * time.Millisecond):
						canceled <- false
					}
				}))
				srv.EnableHTTP2 = http2
				if http2 {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				defer srv.Close()
				c := &Client{Wire: wire, BaseURL: srv.URL, APIKey: "test", Model: "gpt-5-mini", HTTP: srv.Client(), Stream: true}
				msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{})
				if err != nil || msg.Content != "ok" || msg.Usage.OutputTokens != 1 {
					t.Fatalf("message=%+v err=%v", msg, err)
				}
				if <-canceled {
					t.Fatal("client canceled before gateway could finish normally")
				}
			})
		}
	}
}

type blockedStreamBody struct {
	reading chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (b *blockedStreamBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockedStreamBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestStreamFinishBoundsLingeringBody(t *testing.T) {
	body := &blockedStreamBody{reading: make(chan struct{}), closed: make(chan struct{})}
	done := make(chan struct{})
	start := time.Now()
	go func() { finishStream(t.Context(), body, true); close(done) }()
	select {
	case <-done:
		if time.Since(start) < streamFinishGrace/2 {
			t.Fatal("did not allow a finish grace period")
		}
	case <-time.After(2 * time.Second):
		_ = body.Close()
		t.Fatal("cleanup hung on a lingering body")
	}
}

func TestStreamFinishHonorsCancellation(t *testing.T) {
	body := &blockedStreamBody{reading: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { finishStream(ctx, body, true); close(done) }()
	<-body.reading
	cancel()
	select {
	case <-done:
	case <-time.After(streamFinishGrace):
		t.Fatal("cleanup did not honor cancellation")
	}
}

type countedStreamBody struct {
	io.Reader
	read, closes int
}

func (b *countedStreamBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *countedStreamBody) Close() error { b.closes++; return nil }

func TestStreamFinishByteLimitAndFailure(t *testing.T) {
	for _, complete := range []bool{false, true} {
		body := &countedStreamBody{Reader: strings.NewReader(strings.Repeat("x", 1<<20))}
		finishStream(t.Context(), body, complete)
		want := 0
		if complete {
			want = 64 << 10
		}
		if body.read != want || body.closes != 1 {
			t.Fatalf("complete=%t: read=%d closes=%d", complete, body.read, body.closes)
		}
	}
}
