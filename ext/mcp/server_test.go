package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestMCPServeFlow(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, msgs []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "delegated answer"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mow_prompt","arguments":{"prompt":"do a thing"}}}`,
	}, "\n") + "\n")
	var out bytes.Buffer
	if code := serve(eng, in, &out); code != 0 {
		t.Fatalf("serve code=%d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 { // one response per request with an id; the notification is silent
		t.Fatalf("want 3 responses, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], mcpProtocolVersion) {
		t.Fatalf("initialize: %s", lines[0])
	}
	if !strings.Contains(lines[1], "mow_prompt") {
		t.Fatalf("tools/list: %s", lines[1])
	}
	var call struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil {
		t.Fatalf("decode call: %v", err)
	}
	if call.Result.IsError || len(call.Result.Content) == 0 || call.Result.Content[0].Text != "delegated answer" {
		t.Fatalf("tools/call result: %s", lines[2])
	}
}
