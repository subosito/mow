package engine_test

import (
	"context"
	"testing"

	"github.com/subosito/mow"
)

func TestFSChangeEventSummarizesSuccessfulMutations(t *testing.T) {
	calls := 0
	var changes []mow.FileChange
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, AllowWrite: true, OnEvent: func(e mow.Event) {
		if e.Type == mow.EventFSChange {
			changes = e.Files
		}
	}, Chat: func(_ context.Context, _ []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
		calls++
		if calls == 1 {
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{ID: "w", Function: mow.FunctionCall{Name: "write", Arguments: `{"path":"x.txt","content":"hello"}`}}}}, nil
		}
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Prompt(t.Context(), "write"); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "x.txt" || changes[0].Op != "add" {
		t.Fatalf("changes=%+v", changes)
	}
}
