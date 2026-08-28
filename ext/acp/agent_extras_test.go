package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

func TestAgentExtrasInitializeAndMethods(t *testing.T) {
	rw := t.TempDir()
	ro := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession:          true,
		ExtraRoots:         []string{rw},
		ExtraRootsReadOnly: []string{ro},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{
				Role:    "assistant",
				Content: "ok",
				Usage:   mow.Usage{InputTokens: 7, OutputTokens: 2},
			}, nil
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
	var mu sync.Mutex
	var updates []string
	cl.onNotify = func(method string, params json.RawMessage) {
		if method == "session/update" {
			mu.Lock()
			updates = append(updates, string(params))
			mu.Unlock()
		}
	}
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
	for _, name := range []string{"steer", "compact", "rewind", "skill", "plugin", "transcript", "status", "context", "proc", "ping", "slash"} {
		if v, ok := init.AgentCapabilities.Experimental[name]; !ok || v != true {
			t.Fatalf("experimental.%s=%v want true", name, v)
		}
	}
	for _, name := range []string{"steer", "compact", "rewind", "skill.list", "skill.activate", "plugin.list", "transcript", "status", "context", "proc.list", "ping", "slash"} {
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
	if strings.Contains(string(msg["result"]), `"logout"`) {
		t.Fatalf("initialize advertised stub auth.logout: %s", msg["result"])
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

	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	type extraCall struct {
		method string
		params map[string]any
		check  func(t *testing.T, result json.RawMessage)
	}
	for _, tc := range []extraCall{
		{
			method: "steer",
			params: map[string]any{"text": "stay on the tests"},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					OK bool `json:"ok"`
				}
				if err := json.Unmarshal(result, &out); err != nil || !out.OK {
					t.Fatalf("steer result=%s err=%v", result, err)
				}
			},
		},
		{
			method: "compact",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Layer  string `json:"layer"`
					Tokens int    `json:"tokens"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("compact result=%s: %v", result, err)
				}
			},
		},
		{
			method: "rewind",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					OK bool `json:"ok"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("rewind result=%s: %v", result, err)
				}
			},
		},
		{
			method: "transcript",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("transcript result=%s: %v", result, err)
				}
				if out.Messages == nil {
					t.Fatalf("transcript messages nil: %s", result)
				}
			},
		},
		{
			method: "status",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Busy       bool   `json:"busy"`
					Mode       string `json:"mode"`
					AskMode    bool   `json:"ask_mode"`
					Pending    int    `json:"pending_perm"`
					PendingACP int    `json:"pending_permission"`
					ExtraRoots []struct {
						Path     string `json:"path"`
						ReadOnly bool   `json:"read_only"`
					} `json:"extra_roots"`
					Procs []map[string]any `json:"procs"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("status result=%s: %v", result, err)
				}
				if out.Mode != "code" && out.Mode != "ask" {
					t.Fatalf("status.mode=%q want ask|code", out.Mode)
				}
				if out.AskMode != (out.Mode == "ask") {
					t.Fatalf("ask_mode=%v inconsistent with mode=%s", out.AskMode, out.Mode)
				}
				if out.Pending != out.PendingACP {
					t.Fatalf("pending_perm=%d pending_permission=%d", out.Pending, out.PendingACP)
				}
				if out.Procs == nil {
					t.Fatalf("status procs nil: %s", result)
				}
				wantRW, wantRO := filepath.Clean(rw), filepath.Clean(ro)
				gotRW, gotRO := false, false
				for _, root := range out.ExtraRoots {
					path := filepath.Clean(root.Path)
					if path == wantRW && !root.ReadOnly {
						gotRW = true
					}
					if path == wantRO && root.ReadOnly {
						gotRO = true
					}
				}
				if !gotRW || !gotRO {
					t.Fatalf("extra_roots=%v want rw=%s ro=%s", out.ExtraRoots, wantRW, wantRO)
				}
			},
		},
		{
			method: "context",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Tokens int `json:"tokens"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("context result=%s: %v", result, err)
				}
			},
		},
		{
			method: "proc.list",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Items []map[string]any `json:"items"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("proc.list result=%s: %v", result, err)
				}
				if out.Items == nil {
					t.Fatalf("proc.list items nil: %s", result)
				}
			},
		},
		{
			method: "skill.list",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Skills []string         `json:"skills"`
					Items  []map[string]any `json:"items"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("skill.list result=%s: %v", result, err)
				}
				if out.Skills == nil || out.Items == nil {
					t.Fatalf("skill.list empty arrays required: %s", result)
				}
			},
		},
		{
			method: "plugin.list",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var out struct {
					Plugins []string         `json:"plugins"`
					Items   []map[string]any `json:"items"`
				}
				if err := json.Unmarshal(result, &out); err != nil {
					t.Fatalf("plugin.list result=%s: %v", result, err)
				}
				if out.Plugins == nil || out.Items == nil {
					t.Fatalf("plugin.list empty arrays required: %s", result)
				}
			},
		},
		{
			method: "ping",
			params: map[string]any{},
			check: func(t *testing.T, result json.RawMessage) {
				t.Helper()
				var pong string
				if err := json.Unmarshal(result, &pong); err != nil || pong != "pong" {
					t.Fatalf("ping result=%s err=%v", result, err)
				}
			},
		},
	} {
		got, err := cl.call(ctx, tc.method, tc.params)
		if err != nil {
			t.Fatalf("%s: %v", tc.method, err)
		}
		tc.check(t, got["result"])
	}
	if err := cl.callOK(ctx, "slash", map[string]any{"name": "nosuch"}); err == nil {
		t.Fatal("unknown slash should fail")
	}

	stop, usage, err := cl.prompt(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("prompt after extras: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop=%q", stop)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 2 {
		t.Fatalf("prompt usage=%+v", usage)
	}
	deadline := time.Now().Add(2 * time.Second)
	sawChunk, sawUsage := false, false
	for time.Now().Before(deadline) && (!sawChunk || !sawUsage) {
		mu.Lock()
		for _, u := range updates {
			if strings.Contains(u, "agent_message_chunk") && strings.Contains(u, "ok") {
				sawChunk = true
			}
			if strings.Contains(u, "usage_update") {
				sawUsage = true
			}
		}
		mu.Unlock()
		if sawChunk && sawUsage {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawChunk {
		t.Fatal("missing agent_message_chunk for prompt")
	}
	if !sawUsage {
		t.Fatal("missing usage_update for prompt")
	}
	cancel()
	_ = aw.Close()
}

func TestAgentExtrasCancelUnblocks(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			<-ctx.Done()
			return mow.Message{}, ctx.Err()
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

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	type res struct {
		stop string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		stop, _, err := cl.prompt(ctx, sid, "hang")
		ch <- res{stop, err}
	}()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("prompt: %v", r.err)
			}
			if r.stop != "cancelled" {
				t.Fatalf("stop=%q, want cancelled", r.stop)
			}
			cancel()
			_ = aw.Close()
			return
		case <-tick.C:
			_ = cl.notify("session/cancel", map[string]any{"sessionId": sid})
		case <-ctx.Done():
			t.Fatal("timeout: session/cancel never unblocked session/prompt")
		}
	}
}

func TestAgentCompactDoesNotBlockPing(t *testing.T) {
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

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.sessionNew(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	type callResult struct {
		err error
	}
	compactCh := make(chan callResult, 1)
	go func() {
		_, err := cl.call(ctx, "compact", map[string]any{})
		compactCh <- callResult{err: err}
	}()
	pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
	defer pingCancel()
	if err := cl.callOK(pingCtx, "ping", map[string]any{}); err != nil {
		t.Fatalf("ping while compact in flight: %v", err)
	}
	select {
	case r := <-compactCh:
		if r.err != nil {
			t.Fatalf("compact: %v", r.err)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for compact")
	}
	cancel()
	_ = aw.Close()
}
