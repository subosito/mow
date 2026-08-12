package acp

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestPermissionResultReject(t *testing.T) {
	c := &Client{PermissionMode: PermissionReject}
	out := c.permissionResult(nil)["outcome"].(map[string]any)
	if out["outcome"] != "cancelled" {
		t.Fatalf("outcome=%v want cancelled", out["outcome"])
	}
	if _, ok := out["optionId"]; ok {
		t.Fatalf("cancelled must not set optionId: %v", out)
	}
}

func TestPermissionResultAllowPicksOptionID(t *testing.T) {
	c := &Client{PermissionMode: PermissionAllow}
	params := mustJSON(map[string]any{
		"options": []map[string]any{
			{"optionId": "reject-once", "kind": "reject_once", "name": "Reject"},
			{"optionId": "allow-once", "kind": "allow_once", "name": "Allow"},
		},
	})
	out := c.permissionResult(params)["outcome"].(map[string]any)
	if out["outcome"] != "selected" || out["optionId"] != "allow-once" {
		t.Fatalf("outcome=%v", out)
	}
}

func TestPermissionResultAllowWithoutOptionsIsCancelled(t *testing.T) {
	c := &Client{PermissionMode: PermissionAllow}
	out := c.permissionResult(nil)["outcome"].(map[string]any)
	if out["outcome"] != "cancelled" {
		t.Fatalf("without options want cancelled, got %v", out)
	}
}

func TestParseIncomingLineKinds(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`{"jsonrpc":"2.0","method":"session/update","params":{}}`, "notification"},
		{`{"jsonrpc":"2.0","id":"1","method":"cursor/askQuestion","params":{}}`, "request"},
		{`{"jsonrpc":"2.0","id":"2","result":{}}`, "response"},
	}
	for _, tc := range cases {
		got, _, _, ok := parseIncomingLine(tc.line)
		if !ok || got != tc.want {
			t.Fatalf("line=%q got=%q ok=%v want=%q", tc.line, got, ok, tc.want)
		}
	}
}

func TestClientAnswersAgentRequests(t *testing.T) {
	peerIn, clientOut := io.Pipe()
	clientIn, peerOut := io.Pipe()
	c := &Client{
		stdin:          clientOut,
		stdout:         clientIn,
		pending:        map[string]chan response{},
		started:        true,
		exited:         make(chan struct{}),
		PermissionMode: PermissionReject,
	}
	go c.readLoop()

	tests := []struct {
		method   string
		params   map[string]any
		wantErr  bool
		contains []string
	}{
		{"session/request_permission", map[string]any{
			"options": []map[string]any{
				{"optionId": "allow-once", "kind": "allow_once", "name": "Allow"},
			},
		}, false, []string{`"cancelled"`}},
		{"fs/read_text_file", map[string]any{}, true, []string{"filesystem access not available"}},
		{"cursor/askQuestion", map[string]any{}, true, []string{"cursor extension", "not supported"}},
		{"unknown/method", map[string]any{}, true, []string{"method not supported"}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.method, func(t *testing.T) {
			id := tc.method
			req := request{JSONRPC: "2.0", ID: json.RawMessage(`"` + id + `"`), Method: tc.method, Params: mustJSON(tc.params)}
			if err := json.NewEncoder(peerOut).Encode(req); err != nil {
				t.Fatal(err)
			}
			sc := bufio.NewScanner(peerIn)
			sc.Buffer(make([]byte, 0, 4096), 1<<20)
			if !sc.Scan() {
				t.Fatal("no response")
			}
			line := sc.Text()
			for _, sub := range tc.contains {
				if !strings.Contains(line, sub) {
					t.Fatalf("response %q missing %q", line, sub)
				}
			}
			if tc.wantErr && !strings.Contains(line, `"error"`) {
				t.Fatalf("expected error response: %s", line)
			}
			if !tc.wantErr && strings.Contains(line, `"error"`) {
				t.Fatalf("unexpected error response: %s", line)
			}
			// Echoed request id must match (string form).
			if !strings.Contains(line, `"`+id+`"`) {
				t.Fatalf("response missing request id %q: %s", id, line)
			}
		})
	}
}

func TestClientAllowPermissionSelectsOption(t *testing.T) {
	peerIn, clientOut := io.Pipe()
	clientIn, peerOut := io.Pipe()
	c := &Client{
		stdin:          clientOut,
		stdout:         clientIn,
		pending:        map[string]chan response{},
		started:        true,
		exited:         make(chan struct{}),
		PermissionMode: PermissionAllow,
	}
	go c.readLoop()
	req := request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"perm-1"`),
		Method:  "session/request_permission",
		Params: mustJSON(map[string]any{
			"options": []map[string]any{
				{"optionId": "reject-once", "kind": "reject_once", "name": "Reject"},
				{"optionId": "allow-once", "kind": "allow_once", "name": "Allow once"},
			},
		}),
	}
	if err := json.NewEncoder(peerOut).Encode(req); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(peerIn)
	if !sc.Scan() {
		t.Fatal("no response")
	}
	line := sc.Text()
	if !strings.Contains(line, `"selected"`) || !strings.Contains(line, `"allow-once"`) {
		t.Fatalf("want selected allow-once, got %s", line)
	}
	if strings.Contains(line, `"error"`) {
		t.Fatalf("unexpected error: %s", line)
	}
}

func TestSanitizeStderrTail(t *testing.T) {
	in := "api_key=supersecret123\nnormal line\nBearer abc.def.ghi"
	out := sanitizeStderrTail(in)
	if strings.Contains(out, "supersecret123") || strings.Contains(out, "abc.def.ghi") {
		t.Fatalf("secrets leaked: %q", out)
	}
	if !strings.Contains(out, "normal line") {
		t.Fatalf("lost benign line: %q", out)
	}
}

func TestStderrRingCap(t *testing.T) {
	r := newStderrRing(32)
	if _, err := r.Write([]byte(strings.Repeat("a", 100))); err != nil {
		t.Fatal(err)
	}
	if len(r.buf) > 32 {
		t.Fatalf("len=%d cap=32", len(r.buf))
	}
}

func TestAppendStderrHint(t *testing.T) {
	err := appendStderrHint(io.ErrClosedPipe, "boom")
	if err == nil || !strings.Contains(err.Error(), "peer stderr") {
		t.Fatalf("err=%v", err)
	}
}
