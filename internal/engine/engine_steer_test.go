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

// A steer sent once the run is observably started must not be dropped. Hosts
// react to EventRunStart (or Status().Busy) to enable their steer affordance,
// so the run-start reset of the steer buffer must happen before the run is
// visible — otherwise a steer aimed at a demonstrably running turn is wiped.
func TestSteerOnRunStartNotDropped(t *testing.T) {
	turn := 0
	var sawSteer bool
	var eng *mow.Engine
	var err error
	eng, err = mow.New(mow.Options{
		NoSession:  true,
		AllowShell: true,
		Chat: func(ctx context.Context, msgs []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			turn++
			for _, m := range msgs {
				if strings.Contains(m.Content, "START-STEER") {
					sawSteer = true
				}
			}
			if turn == 1 {
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
	// Steer the instant the run is announced — the earliest a host can act.
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventRunStart {
			eng.Steer("START-STEER")
		}
	})
	if _, err := eng.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if !sawSteer {
		t.Fatal("steer sent at EventRunStart was silently dropped")
	}
}
