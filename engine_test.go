package mow_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestSetModelRequiresLiveClient(t *testing.T) {
	// No model from env or $MOW_HOME (TestMain isolates home).
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.SetModel("other"); err == nil {
		t.Fatal("expected error with custom Chat")
	}
	list, err := eng.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// custom Chat with no configured model → empty list
	if len(list) != 0 {
		t.Fatalf("list=%v", list)
	}
}

// TestPromptDoesNotBlockModelDuringChat is the concurrent-status regression:
// eng.Model() must not block while Prompt waits on the LLM.
// Previously Prompt held e.mu for the whole turn; hosts could not read status.
func TestOptionsOverrideModelWorkspace(t *testing.T) {
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Workspace: dir,
		Model:     "override-model",
		BaseURL:   "http://127.0.0.1:9/v1",
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if eng.Workspace() != dir {
		t.Fatalf("workspace=%q want %q", eng.Workspace(), dir)
	}
	if eng.Model() != "override-model" {
		t.Fatalf("model=%q", eng.Model())
	}
}

func TestMaxTurnsStopReason(t *testing.T) {
	n := 0
	eng, err := mow.New(mow.Options{
		NoSession: true,
		MaxTurns:  2,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			return mow.Message{
				Role: "assistant",
				ToolCalls: []mow.ToolCall{{
					ID: "1", Type: "function",
					Function: mow.FunctionCall{Name: "read", Arguments: `{"path":"nope"}`},
				}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Prompt(context.Background(), "loop")
	if err == nil {
		t.Fatal("want max turns error")
	}
	if res.StopReason != mow.StopMaxTurns {
		t.Fatalf("stop=%q", res.StopReason)
	}
}

func TestVersionStringNonEmpty(t *testing.T) {
	if s := mow.VersionString(); s == "" || !strings.HasPrefix(s, "mow ") {
		t.Fatalf("VersionString=%q", s)
	}
}

func TestPromptDoesNotBlockModelDuringChat(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return mow.Message{}, ctx.Err()
			}
			return mow.Message{Role: "assistant", Content: "done"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, e := eng.Prompt(context.Background(), "hello")
		errCh <- e
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("chat never started")
	}

	// Model() used to block here for the entire LLM wait.
	done := make(chan struct{})
	go func() {
		_ = eng.Model()
		_ = eng.Wire()
		_ = eng.SessionID()
		close(done)
	}()
	select {
	case <-done:
		// ok — lock not held during chat
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Model/Wire blocked while Prompt in flight")
	}

	close(release)
	select {
	case e := <-errCh:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not finish")
	}
}

func TestEngineMultiTurn(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		n++
		var last string
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				last = messages[i].Content
				break
			}
		}
		return mow.Message{Role: "assistant", Content: "echo:" + last}, nil
	}
	eng, err := mow.New(mow.Options{NoSession: true, Chat: chat})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := eng.Prompt(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Text != "echo:one" {
		t.Fatalf("r1=%q", r1.Text)
	}
	r2, err := eng.Prompt(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	if r2.Text != "echo:two" {
		t.Fatalf("r2=%q", r2.Text)
	}
	if n != 2 {
		t.Fatalf("turns=%d", n)
	}
}

func TestPromptOptsExtraTools(t *testing.T) {
	var saw []string
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, ts := range tools {
				saw = append(saw, ts.Function.Name)
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	for _, n := range saw {
		if n == "only_this_prompt" {
			t.Fatal("extra tool leaked into default Prompt")
		}
	}
	saw = nil
	if _, err := eng.PromptWith(context.Background(), "hi", mow.PromptOpts{
		ExtraTools: []mow.Tool{extraPromptTool{}},
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range saw {
		if n == "only_this_prompt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ExtraTools missing from specs: %v", saw)
	}
}

type extraPromptTool struct{}

func (extraPromptTool) Name() string                         { return "only_this_prompt" }
func (extraPromptTool) Description() string                  { return "test" }
func (extraPromptTool) Parameters() json.RawMessage          { return json.RawMessage(`{}`) }
func (extraPromptTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
func (extraPromptTool) ReadOnly() bool { return true }

func TestPromptWithSystemAppend(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	var sawSys string
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "system" {
					sawSys = m.Content
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.PromptWith(context.Background(), "hi", mow.PromptOpts{
		SystemAppend: "GOAL_PROTOCOL_MARKER",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawSys, "GOAL_PROTOCOL_MARKER") {
		t.Fatalf("system missing append: %q", sawSys)
	}
}
