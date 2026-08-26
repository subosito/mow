package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

func TestAgentExtrasInitializeAndMethods(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()
	cl := newPipeClient(cr, aw)
	go cl.readLoop()

	msg, err := cl.call(ctx, "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	var init struct {
		AgentCapabilities struct {
			Experimental map[string]any `json:"experimental"`
			Extras       []string       `json:"extras"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(msg["result"], &init); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	for _, name := range []string{"steer", "compact", "rewind"} {
		if v, ok := init.AgentCapabilities.Experimental[name]; !ok || v != true {
			t.Fatalf("experimental.%s=%v want true", name, v)
		}
	}
	for _, name := range []string{"skill", "plugin"} {
		if v, ok := init.AgentCapabilities.Experimental[name]; !ok || v != true {
			t.Fatalf("experimental.%s=%v want true", name, v)
		}
	}
	for _, name := range []string{"steer", "compact", "rewind", "skill.list", "skill.activate", "plugin.list"} {
		found := false
		for _, extra := range init.AgentCapabilities.Extras {
			if extra == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("extras missing %q: %v", name, init.AgentCapabilities.Extras)
		}
	}

	unknown, err := cl.call(ctx, "foo/bar", map[string]any{})
	if err == nil {
		t.Fatalf("unknown method succeeded: %v", unknown)
	}
	if !strings.Contains(err.Error(), "-32601") && !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("unknown method err=%v", err)
	}

	if err := cl.callOK(ctx, "steer", map[string]any{}); err == nil {
		t.Fatal("empty steer should fail")
	}
	if err := cl.callOK(ctx, "steer", map[string]any{"text": "stay on the tests"}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := cl.call(ctx, "compact", map[string]any{}); err != nil {
		t.Fatalf("compact: %v", err)
	}
	rew, err := cl.call(ctx, "rewind", map[string]any{})
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	var rewRes struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(rew["result"], &rewRes)

	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	stop, _, err := cl.prompt(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("prompt after extras: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop=%q", stop)
	}
	cancel()
	_ = aw.Close()
}
