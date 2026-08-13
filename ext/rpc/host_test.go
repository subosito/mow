package rpc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/rpc"
	"github.com/subosito/mow/slash"
)

// serveLines runs a server over the given request lines and returns every
// emitted line, decoded loosely.
func serveLines(t *testing.T, eng *mow.Engine, lines ...string) []map[string]json.RawMessage {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	srv := &rpc.Server{Engine: eng, In: in, Out: &out, StreamEvents: new(bool)}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line not json: %q", line)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func newEcho(t *testing.T, opts mow.Options) *mow.Engine {
	t.Helper()
	if opts.Chat == nil {
		opts.Chat = func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		}
	}
	opts.NoSession = true
	eng, err := mow.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func resultOf(t *testing.T, msgs []map[string]json.RawMessage, id string) (json.RawMessage, *struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}) {
	t.Helper()
	for _, m := range msgs {
		if string(m["id"]) != id {
			continue
		}
		if raw, ok := m["error"]; ok {
			var e struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}
			return nil, &e
		}
		return m["result"], nil
	}
	t.Fatalf("no response for id %s: %v", id, msgs)
	return nil, nil
}

func TestRPCHostReadMethods(t *testing.T) {
	eng := newEcho(t, mow.Options{})

	msgs := serveLines(t,
		eng,
		`{"id":1,"method":"sessions"}`,
		`{"id":2,"method":"prompt","params":{"text":"hello"}}`,
		`{"id":4,"method":"slash.list"}`,
	)
	// transcript is a control method (concurrent with prompt), so read it in a
	// second pass once the turn above has finished.
	msgs = append(msgs, serveLines(t, eng, `{"id":3,"method":"transcript"}`)...)

	// sessions: NoSession engine has none, but the shape must hold.
	res, rerr := resultOf(t, msgs, "1")
	if rerr != nil {
		t.Fatalf("sessions error: %v", rerr)
	}
	var sess struct {
		Sessions []struct {
			ID      string `json:"id"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(res, &sess); err != nil {
		t.Fatalf("sessions shape: %v (%s)", err, res)
	}

	// prompt: usage present alongside the pre-existing fields.
	res, rerr = resultOf(t, msgs, "2")
	if rerr != nil {
		t.Fatalf("prompt error: %v", rerr)
	}
	var pr struct {
		Text  string `json:"text"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(res, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Text != "hi" || pr.Usage == nil {
		t.Fatalf("prompt result=%s", res)
	}

	// transcript: the turn we just ran is visible to a resuming UI.
	res, rerr = resultOf(t, msgs, "3")
	if rerr != nil {
		t.Fatalf("transcript error: %v", rerr)
	}
	var tr struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(res, &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Messages) == 0 {
		t.Fatalf("expected transcript messages: %s", res)
	}
	var sawUser, sawAssistant bool
	for _, m := range tr.Messages {
		switch m.Role {
		case "user":
			sawUser = sawUser || m.Content == "hello"
		case "assistant":
			sawAssistant = sawAssistant || m.Content == "hi"
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("transcript missing turn: %s", res)
	}

	// slash.list: may be empty when no pack is linked; shape must decode and
	// must not carry Usage.
	res, rerr = resultOf(t, msgs, "4")
	if rerr != nil {
		t.Fatalf("slash.list error: %v", rerr)
	}
	var sl struct {
		Commands []map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(res, &sl); err != nil {
		t.Fatalf("slash.list shape: %v (%s)", err, res)
	}
	for _, c := range sl.Commands {
		for _, k := range []string{"name", "summary", "exclusive", "aliases"} {
			if _, ok := c[k]; !ok {
				t.Fatalf("slash.list entry missing %s: %v", k, c)
			}
		}
		if _, ok := c["usage"]; ok {
			t.Fatalf("slash.list must not include usage: %v", c)
		}
	}
}

func TestRPCSteer(t *testing.T) {
	eng := newEcho(t, mow.Options{})
	msgs := serveLines(t, eng,
		`{"id":1,"method":"steer","params":{"text":"focus on tests"}}`,
		`{"id":2,"method":"steer","params":{"text":"  "}}`,
		`{"id":3,"method":"steer"}`,
	)
	if res, rerr := resultOf(t, msgs, "1"); rerr != nil || !strings.Contains(string(res), `"ok":true`) {
		t.Fatalf("steer res=%s err=%v", res, rerr)
	}
	for _, id := range []string{"2", "3"} {
		if _, rerr := resultOf(t, msgs, id); rerr == nil {
			t.Fatalf("empty steer text must be invalid (id %s)", id)
		}
	}
}

func TestRPCSlash(t *testing.T) {
	slash.Register(slash.Command{
		Name:    "rpctest",
		Aliases: []string{"rt"},
		Summary: "test command",
		Usage:   "usage: /rpctest [args]",
		Run: func(ctx context.Context, req slash.Request) (slash.Result, error) {
			return slash.Result{Title: "rpctest · " + req.Invoked, Body: strings.Join(req.Args, ",")}, nil
		},
	})

	eng := newEcho(t, mow.Options{})
	msgs := serveLines(t, eng,
		`{"id":1,"method":"slash","params":{"name":"rt","args":["a","b"]}}`,
		`{"id":2,"method":"slash","params":{"name":"nope-not-a-command"}}`,
		`{"id":3,"method":"slash","params":{"name":"rpctest","args":["help"]}}`,
		`{"id":4,"method":"slash","params":{}}`,
	)

	res, rerr := resultOf(t, msgs, "1")
	if rerr != nil {
		t.Fatalf("slash error: %v", rerr)
	}
	var out struct{ Title, Body string }
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	if out.Title != "rpctest · rt" || out.Body != "a,b" {
		t.Fatalf("slash result=%s", res)
	}

	if _, rerr := resultOf(t, msgs, "2"); rerr == nil {
		t.Fatal("unknown slash command must error")
	}

	res, rerr = resultOf(t, msgs, "3")
	if rerr != nil {
		t.Fatalf("slash help error: %v", rerr)
	}
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Body, "usage: /rpctest") {
		t.Fatalf("help body=%s", res)
	}

	if _, rerr := resultOf(t, msgs, "4"); rerr == nil {
		t.Fatal("missing slash name must error")
	}
}

func TestRPCSlashExclusiveWhileBusy(t *testing.T) {
	slash.Register(slash.Command{
		Name:      "rpcexcl",
		Summary:   "exclusive test command",
		Exclusive: true,
		Run: func(ctx context.Context, req slash.Request) (slash.Result, error) {
			return slash.Result{Title: "ran"}, nil
		},
	})

	started := make(chan struct{})
	release := make(chan struct{})
	eng := newEcho(t, mow.Options{
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return mow.Message{}, ctx.Err()
			}
			return mow.Message{Role: "assistant", Content: "done"}, nil
		},
	})

	pr, pw := bytesNewPipe()
	out := &syncBuf{}
	srv := &rpc.Server{Engine: eng, In: pr, Out: out, StreamEvents: new(bool)}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background()) }()

	_, _ = pw.WriteString(`{"id":1,"method":"prompt","params":{"text":"block"}}` + "\n")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not start")
	}
	_, _ = pw.WriteString(`{"id":2,"method":"slash","params":{"name":"rpcexcl"}}` + "\n")

	deadline := time.After(3 * time.Second)
	for {
		if strings.Contains(out.String(), "exclusive slash command") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected exclusive refusal: %s", out.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(release)
	_ = pw.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not finish")
	}
}

// writeChat drives one write tool call, then reports what the tool result said.
func writeChat(path string) func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
	var step int
	return func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		step++
		if step == 1 {
			args, _ := json.Marshal(map[string]string{"path": path, "content": "hello\n"})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "call-1", Type: "function",
				Function: mow.FunctionCall{Name: "write", Arguments: string(args)},
			}}}, nil
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "tool" {
				return mow.Message{Role: "assistant", Content: "tool:" + messages[i].Content}, nil
			}
		}
		return mow.Message{Role: "assistant", Content: "no tool result"}, nil
	}
}

func TestRPCPermDefaultAllows(t *testing.T) {
	dir := t.TempDir()
	eng := newEcho(t, mow.Options{
		Workspace:  dir,
		AllowWrite: true,
		Chat:       writeChat("note.txt"),
	})
	msgs := serveLines(t, eng, `{"id":1,"method":"prompt","params":{"text":"write it"}}`)
	if _, rerr := resultOf(t, msgs, "1"); rerr != nil {
		t.Fatalf("prompt error: %v", rerr)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err != nil {
		t.Fatalf("default policy must stay fail-open: %v", err)
	}
}

func TestRPCPermAskModeDecisions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decision  string
		wantFile  bool
		wantInRes string
	}{
		{name: "allow", decision: "allow", wantFile: true},
		{name: "always", decision: "always", wantFile: true},
		{name: "deny", decision: "deny", wantFile: false, wantInRes: "denied by user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			eng := newEcho(t, mow.Options{
				Workspace:  dir,
				AllowWrite: true,
				Chat:       writeChat("note.txt"),
			})

			pr, pw := bytesNewPipe()
			outR, outW := io.Pipe()
			srv := &rpc.Server{Engine: eng, In: pr, Out: outW, StreamEvents: new(bool)}
			done := make(chan error, 1)
			go func() { done <- srv.Serve(context.Background()) }()

			lines := make(chan map[string]json.RawMessage, 32)
			go func() {
				defer close(lines)
				sc := bufio.NewScanner(outR)
				for sc.Scan() {
					var m map[string]json.RawMessage
					if err := json.Unmarshal(sc.Bytes(), &m); err == nil {
						lines <- m
					}
				}
			}()

			_, _ = pw.WriteString(`{"id":1,"method":"perm.set","params":{"mode":"ask"}}` + "\n")
			_, _ = pw.WriteString(`{"id":2,"method":"prompt","params":{"text":"write it"}}` + "\n")

			var askID string
			var promptRes json.RawMessage
			timeout := time.After(5 * time.Second)
			for promptRes == nil {
				select {
				case m, ok := <-lines:
					if !ok {
						t.Fatalf("output closed before prompt result")
					}
					if string(m["method"]) == `"perm.ask"` {
						var p struct {
							ID   string          `json:"id"`
							Name string          `json:"name"`
							Args json.RawMessage `json:"args"`
						}
						if err := json.Unmarshal(m["params"], &p); err != nil {
							t.Fatal(err)
						}
						if p.Name != "write" || len(p.Args) == 0 {
							t.Fatalf("bad perm.ask params: %s", m["params"])
						}
						askID = p.ID
						_, _ = pw.WriteString(`{"id":3,"method":"perm.decide","params":{"id":"` + askID + `","decision":"` + tc.decision + `"}}` + "\n")
						continue
					}
					if string(m["id"]) == "2" {
						promptRes = m["result"]
						if promptRes == nil {
							t.Fatalf("prompt failed: %s", m["error"])
						}
					}
				case <-timeout:
					t.Fatal("timed out waiting on perm flow")
				}
			}
			if askID == "" {
				t.Fatal("ask mode did not emit perm.ask")
			}
			if tc.wantInRes != "" && !strings.Contains(string(promptRes), tc.wantInRes) {
				t.Fatalf("result=%s want %q", promptRes, tc.wantInRes)
			}
			_, err := os.Stat(filepath.Join(dir, "note.txt"))
			if got := err == nil; got != tc.wantFile {
				t.Fatalf("file exists=%v want %v (err %v)", got, tc.wantFile, err)
			}

			_ = pw.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("serve did not finish")
			}
			_ = outW.Close()
		})
	}
}

func TestRPCPermDecideUnknownID(t *testing.T) {
	eng := newEcho(t, mow.Options{})
	msgs := serveLines(t, eng,
		`{"id":1,"method":"perm.decide","params":{"id":"perm-999","decision":"allow"}}`,
		`{"id":2,"method":"perm.set","params":{"mode":"weird"}}`,
	)
	if _, rerr := resultOf(t, msgs, "1"); rerr == nil {
		t.Fatal("unknown permission id must error")
	}
	if _, rerr := resultOf(t, msgs, "2"); rerr == nil {
		t.Fatal("invalid perm mode must error")
	}
}

// syncBuf is a concurrency-safe writer for tests that poll output.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
