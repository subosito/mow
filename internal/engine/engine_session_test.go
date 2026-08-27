package engine_test

import (
	"context"
	"testing"

	"github.com/subosito/mow"
)

func TestBeginSessionClearsNoSessionHistory(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Prompt(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if len(eng.Transcript()) == 0 {
		t.Fatal("want transcript after prompt")
	}
	id, err := eng.BeginSession()
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("NoSession BeginSession id=%q want empty", id)
	}
	if got := eng.Transcript(); len(got) != 0 {
		t.Fatalf("transcript after BeginSession: %v", got)
	}
}

func TestBeginSessionRotatesJSONL(t *testing.T) {
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	first := eng.SessionID()
	if first == "" {
		t.Fatal("want session id")
	}
	if _, err := eng.Prompt(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	second, err := eng.BeginSession()
	if err != nil {
		t.Fatal(err)
	}
	if second == "" || second == first {
		t.Fatalf("BeginSession=%q first=%q", second, first)
	}
	if got := eng.Transcript(); len(got) != 0 {
		t.Fatalf("transcript after rotate: %v", got)
	}
	if eng.SessionID() != second {
		t.Fatalf("SessionID=%q want %q", eng.SessionID(), second)
	}
}
