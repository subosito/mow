package engine_test

import (
	"context"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func TestDelegatePeerDoesNotContinueOnBeforeFinish(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterBeforeFinishSource("test", func(context.Context, ext.BeforeFinishEvent) (ext.BeforeFinishDecision, error) {
		return ext.BeforeFinishDecision{Continue: "run more"}, nil
	})

	calls := 0
	chat := func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		calls++
		if calls == 1 {
			return mow.Message{Role: "assistant", Content: "answer"}, nil
		}
		return mow.Message{Role: "assistant", Content: "continued"}, nil
	}
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		NoSession:      true,
		DelegatePeer:   true,
		Workspace:      t.TempDir(),
		Chat:           chat,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	res, err := eng.Prompt(t.Context(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1 (Stop continuation must not run on a delegate peer)", calls)
	}
	if res.Text != "answer" {
		t.Fatalf("text=%q", res.Text)
	}
}
