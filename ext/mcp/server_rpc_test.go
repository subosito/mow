package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func serveTestEngine(t *testing.T) *mow.Engine {
	t.Helper()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// serveLines runs the server over the given request lines and returns the
// responses it wrote.
func serveLines(t *testing.T, reqs ...string) []string {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(reqs, "\n") + "\n")
	if code := serve(serveTestEngine(t), in, &out); code != 0 {
		t.Fatalf("serve code=%d", code)
	}
	body := strings.TrimSpace(out.String())
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// JSON-RPC 2.0 requires a -32700 response for unparseable input. Dropping the
// line silently left a peer waiting for a reply that never came.
func TestServeRepliesParseErrorOnMalformedJSON(t *testing.T) {
	lines := serveLines(t, `{"jsonrpc":"2.0","id":1,"method":`)
	if len(lines) != 1 {
		t.Fatalf("want one parse-error reply, got %d: %q", len(lines), lines)
	}
	var resp struct {
		ID    any `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("reply is not valid JSON: %s", lines[0])
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Fatalf("want -32700 parse error, got %s", lines[0])
	}
	if resp.ID != nil {
		t.Fatalf("parse error must carry a null id, got %v", resp.ID)
	}
}

// A malformed line must not desynchronise the stream: the next valid request
// is still answered.
func TestServeContinuesAfterParseError(t *testing.T) {
	lines := serveLines(t,
		`{"garbage`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	)
	if len(lines) != 2 {
		t.Fatalf("want parse error + tools/list reply, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[1], "mow_prompt") {
		t.Fatalf("valid request after a bad line was not answered: %s", lines[1])
	}
}

// Unknown methods get -32601, not silence.
func TestServeUnknownMethod(t *testing.T) {
	lines := serveLines(t, `{"jsonrpc":"2.0","id":5,"method":"nope/there","params":{}}`)
	if len(lines) != 1 || !strings.Contains(lines[0], "-32601") {
		t.Fatalf("want method-not-found reply, got %q", lines)
	}
}

// Notifications (no id) stay silent even when the method is unknown.
func TestServeNotificationsAreSilent(t *testing.T) {
	lines := serveLines(t,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/unknown"}`,
	)
	if len(lines) != 0 {
		t.Fatalf("notifications must not be answered, got %q", lines)
	}
}
