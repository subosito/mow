package llm

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamPrematureHTTPEnd(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal=%t", terminal), func(t *testing.T) {
			body := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
			if terminal {
				body += "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
				body += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				// Deliberately truncate the HTTP body after the available SSE frames.
				w.Header().Set("Content-Length", fmt.Sprint(len(body)+100))
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL + "/v1", APIKey: "test-key", Model: "gpt-5-mini", HTTP: srv.Client()}
			msg, err := c.ChatStreamHooks(t.Context(), []Message{{Role: "user", Content: "hello"}}, nil, StreamHooks{})
			if !terminal {
				if !errors.Is(err, io.ErrUnexpectedEOF) || msg.Content != "" {
					t.Fatalf("unconfirmed response: msg=%+v err=%v", msg, err)
				}
				return
			}
			if err != nil || msg.Content != "ok" || msg.StopReason != "stop" || msg.Usage.InputTokens != 7 {
				t.Fatalf("completed response: msg=%+v err=%v", msg, err)
			}
		})
	}
}

func TestChatStreamMalformedCompletionDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"private text\"}}]}\n\ndata: {invalid private payload}\n\n")
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "test-key", Model: "gpt-5-mini", HTTP: srv.Client()}
	_, err := c.ChatStreamHooks(t.Context(), []Message{{Role: "user", Content: "hello"}}, nil, StreamHooks{})
	inc, ok := errors.AsType[*streamIncompleteError](err)
	if !ok || !errors.Is(err, errStreamIncomplete) {
		t.Fatalf("err=%v want *streamIncompleteError", err)
	}
	if d := inc.Detail(); !strings.Contains(d, "malformed=1") || strings.Contains(d, "private") || strings.Contains(err.Error(), "private") {
		t.Fatalf("expected payload-free incomplete diagnostics: err=%q detail=%q", err, d)
	}
}
