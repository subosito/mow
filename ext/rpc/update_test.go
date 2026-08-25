package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/rpc"
)

func TestRPCConfigList(t *testing.T) {
	eng := newEcho(t, mow.Options{Model: "m1"})
	msgs := serveLines(t, eng,
		`{"id":1,"method":"perm.set","params":{"mode":"ask"}}`,
		`{"id":2,"method":"config.list"}`,
	)
	res, err := resultOf(t, msgs, "2")
	if err != nil {
		t.Fatalf("config.list: %v", err)
	}
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Current string `json:"current"`
			Set     string `json:"set"`
		} `json:"items"`
	}
	if json.Unmarshal(res, &out) != nil {
		t.Fatalf("shape: %s", res)
	}
	found := map[string]struct {
		Current string
		Set     string
	}{}
	for _, item := range out.Items {
		found[item.ID] = struct {
			Current string
			Set     string
		}{item.Current, item.Set}
	}
	if p, ok := found["perm"]; !ok || p.Current != "ask" || p.Set != "perm.set" {
		t.Fatalf("perm item=%+v items=%s", p, res)
	}
	if m, ok := found["model"]; !ok || m.Current != "m1" || m.Set != "model.set" {
		t.Fatalf("model item=%+v items=%s", m, res)
	}
}

func TestRPCTypedUpdateAlongsideEvent(t *testing.T) {
	eng := newEcho(t, mow.Options{
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	stream := true
	in := strings.NewReader(`{"id":1,"method":"prompt","params":{"text":"hi"}}` + "\n")
	var out bytes.Buffer
	srv := &rpc.Server{Engine: eng, In: in, Out: &out, StreamEvents: &stream}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawEvent, sawUpdate bool
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		switch string(m["method"]) {
		case `"event"`:
			sawEvent = true
		case `"update"`:
			sawUpdate = true
			var p struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(m["params"], &p) != nil || p.Kind == "" {
				t.Fatalf("bad update: %s", line)
			}
		}
	}
	if !sawEvent || !sawUpdate {
		t.Fatalf("event=%v update=%v out=%s", sawEvent, sawUpdate, out.String())
	}
}
