package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// lspFrames encodes payloads with the Content-Length framing a language server
// uses, so client.call reads them exactly as it would from a real gopls.
func lspFrames(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(p), p)
	}
	return b.String()
}

func fakeClient(frames string) *client {
	return &client{
		stdin:  nopWriteCloser{io.Discard},
		stdout: bufio.NewReader(strings.NewReader(frames)),
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// A server→client request (workspace/configuration, client/registerCapability)
// carries a method *and* an id. Treating it as a reply returned an empty result
// and reported success.
func TestCallSkipsServerRequestWithID(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","id":42,"method":"workspace/configuration","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"contents":"doc"}}`,
	)
	raw, err := fakeClient(frames).call(context.Background(), "textDocument/hover", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var res struct {
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Contents != "doc" {
		t.Fatalf("result was not our reply: %q (%v)", raw, err)
	}
}

// gopls answers a slow request after we have moved on; that late reply must not
// satisfy the next call, or every later request returns the previous answer.
func TestCallSkipsStaleReplyID(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","id":9,"result":{"contents":"stale"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"contents":"fresh"}}`,
	)
	raw, err := fakeClient(frames).call(context.Background(), "textDocument/hover", map[string]any{})
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

// Notifications keep being skipped and a real error reply still surfaces.
func TestCallSkipsNotificationAndReportsError(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"message":"no server"}}`,
	)
	_, err := fakeClient(frames).call(context.Background(), "textDocument/hover", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "no server") {
		t.Fatalf("err = %v, want the server error", err)
	}
}
