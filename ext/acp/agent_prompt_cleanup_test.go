package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/subosito/mow"
)

type promptResponseWriter func([]byte) (int, error)

func (w promptResponseWriter) Write(p []byte) (int, error) { return w(p) }

// Inspect the session slot during the terminal write, before handleRequest's
// deferred cleanup can run. This makes the immediate-next-prompt race deterministic.
func TestPromptReleasesSlotBeforeTerminalResponse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		chatErr  error
		wantCode int
		wantStop string
	}{
		{name: "success", wantStop: "end_turn"},
		{name: "provider error", chatErr: errors.New("provider unavailable"), wantCode: errInternal},
		{name: "cancelled", wantStop: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			eng, err := mow.New(mow.Options{
				Workspace: t.TempDir(), NoSession: true,
				Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
					if tc.name == "cancelled" {
						cancel()
						return mow.Message{}, context.Canceled
					}
					return mow.Message{Role: "assistant", Content: "ok"}, tc.chatErr
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			a := &agentServer{
				eng: eng, boundSID: "session", cancels: map[string]context.CancelFunc{},
				sessions: map[string]*acpSession{"session": {mode: ModeCode}},
			}
			terminal := false
			a.out = promptResponseWriter(func(p []byte) (int, error) {
				var got struct {
					ID     json.RawMessage `json:"id"`
					Error  *rpcError       `json:"error"`
					Result struct {
						StopReason string `json:"stopReason"`
					} `json:"result"`
				}
				if err := json.Unmarshal(p, &got); err != nil {
					t.Errorf("decode response: %v", err)
					return len(p), nil
				}
				if string(got.ID) != "1" {
					return len(p), nil
				}
				terminal = true
				a.mu.Lock()
				busy := a.cancels["session"] != nil || a.activeSID != ""
				a.mu.Unlock()
				if busy {
					t.Error("terminal response published while session still in flight")
				}
				if tc.wantCode != 0 {
					if got.Error == nil || got.Error.Code != tc.wantCode {
						t.Errorf("error=%+v, want code %d", got.Error, tc.wantCode)
					}
				} else if got.Error != nil || got.Result.StopReason != tc.wantStop {
					t.Errorf("response error=%+v stop=%q, want %q", got.Error, got.Result.StopReason, tc.wantStop)
				}
				return len(p), nil
			})
			a.handleRequest(ctx, request{
				JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "session/prompt",
				Params: mustJSON(promptParams{SessionID: "session", Prompt: []ContentBlock{{Type: "text", Text: "hello"}}}),
			})
			if !terminal {
				t.Fatal("no terminal response")
			}
		})
	}
}
