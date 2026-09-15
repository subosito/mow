package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// fakeStdio drives client.call without a real process: it feeds canned server
// frames and captures what the client wrote.
func fakeStdio(t *testing.T, serverFrames string) (*client, *strings.Builder) {
	t.Helper()
	var wrote strings.Builder
	return &client{
		stdin:  nopWriteCloser{&wrote},
		stdout: bufio.NewReader(strings.NewReader(serverFrames)),
	}, &wrote
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// A server→client request (sampling/createMessage, roots/list, …) carries both
// a method and an id. It is not a reply, and returning it as one handed the
// caller an empty result while reporting success.
//
// It must also be ANSWERED: the server can be waiting on it before it finishes
// our call, so silently skipping it deadlocks both sides.
func TestCallAnswersServerRequestAndKeepsWaiting(t *testing.T) {
	frames := `{"jsonrpc":"2.0","id":99,"method":"sampling/createMessage","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n"
	c, wrote := fakeStdio(t, frames)

	raw, err := c.call(context.Background(), "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var res struct {
		Tools []toolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("result was not our reply: %q (%v)", raw, err)
	}
	// The unsupported request got a -32601 keyed to ITS id, not ours.
	sent := wrote.String()
	if !strings.Contains(sent, "-32601") {
		t.Fatalf("server request was not answered — deadlock risk:\n%s", sent)
	}
	if !strings.Contains(sent, `"id":99`) {
		t.Fatalf("rejection did not echo the server's id:\n%s", sent)
	}
}

// Ping is always legal regardless of negotiated capabilities and must get an
// empty result, not an error.
func TestCallAnswersPingWithResult(t *testing.T) {
	frames := `{"jsonrpc":"2.0","id":42,"method":"ping"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"
	c, wrote := fakeStdio(t, frames)

	if _, err := c.call(context.Background(), "tools/list", map[string]any{}); err != nil {
		t.Fatalf("call: %v", err)
	}
	sent := wrote.String()
	if !strings.Contains(sent, `"id":42`) {
		t.Fatalf("ping not answered:\n%s", sent)
	}
	if strings.Contains(sent, "-32601") {
		t.Fatalf("ping must get a result, not an error:\n%s", sent)
	}
}

// A late reply to an abandoned call (ctx cancel, timeout) must not satisfy the
// next call: accepting it shifts every later call one reply behind, so each
// tool returns the previous tool's output.
func TestCallSkipsStaleReplyID(t *testing.T) {
	frames := `{"jsonrpc":"2.0","id":7,"result":{"stale":true}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"fresh":true}}` + "\n"
	c, _ := fakeStdio(t, frames)

	raw, err := c.call(context.Background(), "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if strings.Contains(string(raw), "stale") {
		t.Fatalf("stale reply accepted as this call's result: %s", raw)
	}
	if !strings.Contains(string(raw), "fresh") {
		t.Fatalf("want the id-1 reply, got %s", raw)
	}
}

// Notifications (no id) stay skipped, and a genuine error reply still surfaces.
func TestCallSkipsNotificationAndReportsError(t *testing.T) {
	frames := `{"jsonrpc":"2.0","method":"notifications/message","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"error":{"message":"boom"}}` + "\n"
	c, _ := fakeStdio(t, frames)

	if _, err := c.call(context.Background(), "tools/list", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the server error", err)
	}
}

func TestIsReplyTo(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"matching response", `{"id":3,"result":{}}`, true},
		{"other id", `{"id":4,"result":{}}`, false},
		{"notification", `{"method":"x","params":{}}`, false},
		{"server request with id", `{"id":3,"method":"roots/list"}`, false},
		{"string id", `{"id":"3","result":{}}`, false},
		{"no id", `{"result":{}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m rpcMessage
			if err := json.Unmarshal([]byte(c.raw), &m); err != nil {
				t.Fatal(err)
			}
			if got := m.isReplyTo(3); got != c.want {
				t.Fatalf("isReplyTo(3) = %v, want %v", got, c.want)
			}
		})
	}
}
