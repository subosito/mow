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

func fakeClient(frames string) (*client, *strings.Builder) {
	var wrote strings.Builder
	return &client{
		stdin:  nopWriteCloser{&wrote},
		stdout: bufio.NewReader(strings.NewReader(frames)),
	}, &wrote
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// A server→client request (workspace/configuration, client/registerCapability)
// carries a method *and* an id. Treating it as a reply returned an empty result
// and reported success — and simply skipping it deadlocks, because gopls waits
// for the answer before finishing the request we are blocked on.
func TestCallAnswersConfigurationRequest(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","id":42,"method":"workspace/configuration","params":{"items":[{"section":"gopls"}]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"contents":"doc"}}`,
	)
	c, wrote := fakeClient(frames)
	raw, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var res struct {
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Contents != "doc" {
		t.Fatalf("result was not our reply: %q (%v)", raw, err)
	}
	sent := wrote.String()
	if !strings.Contains(sent, `"id":42`) {
		t.Fatalf("configuration request was not answered — deadlock risk:\n%s", sent)
	}
	// LSP defines null per item as "no configuration"; an error here makes
	// some servers log and fall back noisily.
	if strings.Contains(sent, "-32601") {
		t.Fatalf("workspace/configuration should get null config, not an error:\n%s", sent)
	}
	if !strings.Contains(sent, "Content-Length:") {
		t.Fatalf("reply was not LSP-framed:\n%s", sent)
	}
}

// An unsupported server request still has to be answered, with -32601.
func TestCallRejectsUnsupportedServerRequest(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","id":7,"method":"window/showMessageRequest","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"contents":"doc"}}`,
	)
	c, wrote := fakeClient(frames)
	if _, err := c.call(context.Background(), "textDocument/hover", map[string]any{}); err != nil {
		t.Fatalf("call: %v", err)
	}
	sent := wrote.String()
	if !strings.Contains(sent, `"id":7`) || !strings.Contains(sent, "-32601") {
		t.Fatalf("unsupported request not rejected:\n%s", sent)
	}
}

// gopls answers a slow request after we have moved on; that late reply must not
// satisfy the next call, or every later request returns the previous answer.
func TestCallSkipsStaleReplyID(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","id":9,"result":{"contents":"stale"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"contents":"fresh"}}`,
	)
	c, _ := fakeClient(frames)
	raw, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
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

func TestCallAcceptsStringReplyID(t *testing.T) {
	frames := lspFrames(`{"jsonrpc":"2.0","id":"1","result":{"contents":"doc"}}`)
	c, _ := fakeClient(frames)
	raw, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(raw), "doc") {
		t.Fatalf("string id reply not matched: %s", raw)
	}
}

func TestRpcIDEqualNumberAndString(t *testing.T) {
	if !rpcIDEqual(json.RawMessage(`1`), 1) || !rpcIDEqual(json.RawMessage(`"1"`), 1) {
		t.Fatal("want numeric and string id 1 to match")
	}
	if rpcIDEqual(json.RawMessage(`"2"`), 1) || rpcIDEqual(json.RawMessage(`null`), 1) {
		t.Fatal("mismatched ids must not match")
	}
}

// Notifications keep being skipped and a real error reply still surfaces.
func TestCallSkipsNotificationAndReportsError(t *testing.T) {
	frames := lspFrames(
		`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"message":"no server"}}`,
	)
	c, _ := fakeClient(frames)
	_, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "no server") {
		t.Fatalf("err = %v, want the server error", err)
	}
}
