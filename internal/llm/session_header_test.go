package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionHeaderFromContext(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(HeaderSession)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := &Client{Wire: WireOpenAIChat, BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}

	for _, tc := range []struct {
		name, sid, configured, want string
	}{
		{"context session", "20261001T1-abc", "", "20261001T1-abc"},
		{"no session", "", "", ""},
		{"operator header wins", "20261001T1-abc", "pinned", "pinned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.ExtraHeaders = nil
			if tc.configured != "" {
				c.ExtraHeaders = map[string]string{HeaderSession: tc.configured}
			}
			ctx := WithSessionID(context.Background(), tc.sid)
			if _, err := c.Chat(ctx, []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Fatal(err)
			}
			if h := <-got; h != tc.want {
				t.Fatalf("X-Mow-Session=%q want %q", h, tc.want)
			}
		})
	}
}
