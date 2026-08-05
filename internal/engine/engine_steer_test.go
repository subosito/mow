package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// Mid-run Steer is injected into the running turn; a pre-run Steer is dropped at
// run start (real UIs steer while busy, not before).
func TestSteerInjectsMidTurn(t *testing.T) {
	turn := 0
	var sawMid, sawPre bool
	var eng *mow.Engine
	var err error
	eng, err = mow.New(mow.Options{
		NoSession:  true,
		AllowShell: true,
		Chat: func(ctx context.Context, msgs []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			turn++
			for _, m := range msgs {
				if strings.Contains(m.Content, "MID-STEER") {
					sawMid = true
				}
				if strings.Contains(m.Content, "PRE-STEER") {
					sawPre = true
				}
			}
			if turn == 1 {
				eng.Steer("MID-STEER") // steer while the turn is working
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "1", Type: "function",
					Function: mow.FunctionCall{Name: "bash", Arguments: `{"command":"echo hi"}`},
				}}}, nil
			}
			return mow.Message{Role: "assistant", Content: "done"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.Steer("PRE-STEER") // before the run — should be cleared at run start
	if _, err := eng.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if !sawMid {
		t.Fatal("mid-run steer was not injected")
	}
	if sawPre {
		t.Fatal("pre-run steer should have been dropped at run start")
	}
}
