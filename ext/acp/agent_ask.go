package acp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/subosito/mow"
)

func (a *agentServer) askUser(ctx context.Context, questions []mow.AskUserQuestion) ([]mow.AskUserAnswer, error) {
	raw, err := a.callClient(ctx, "session/request_input", map[string]any{"questions": questions})
	if err != nil {
		return nil, err
	}
	var out struct {
		Answers []mow.AskUserAnswer `json:"answers"`
	}
	dec := json.Unmarshal(raw, &out)
	if dec != nil {
		return nil, fmt.Errorf("ask_user: invalid client response")
	}
	return out.Answers, nil
}
