package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type askUserTool struct{ fn AskUserFunc }

func (askUserTool) Name() string { return "ask_user" }
func (askUserTool) Description() string {
	return "Ask the user up to four structured questions when the next code decision depends on a preference that cannot be inferred. This is not a permission prompt."
}
func (askUserTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["questions"],"properties":{"questions":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","additionalProperties":false,"required":["id","prompt"],"properties":{"id":{"type":"string","maxLength":64},"prompt":{"type":"string","maxLength":1000},"allow_multiple":{"type":"boolean"},"options":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["id","label"],"properties":{"id":{"type":"string","maxLength":64},"label":{"type":"string","maxLength":200}}}}}}}}}`)
}
func (t askUserTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Questions []AskUserQuestion `json:"questions"`
	}
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", err
	}
	if len(in.Questions) < 1 || len(in.Questions) > 4 {
		return "", fmt.Errorf("ask_user: requires 1-4 questions")
	}
	ids := map[string]bool{}
	for _, q := range in.Questions {
		q.ID = strings.TrimSpace(q.ID)
		q.Prompt = strings.TrimSpace(q.Prompt)
		if q.ID == "" || q.Prompt == "" || ids[q.ID] || len(q.ID) > 64 || len(q.Prompt) > 1000 || len(q.Options) > 8 {
			return "", fmt.Errorf("ask_user: invalid or duplicate question")
		}
		ids[q.ID] = true
		opts := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" || opts[o.ID] || len(o.ID) > 64 || len(o.Label) > 200 {
				return "", fmt.Errorf("ask_user: invalid or duplicate option")
			}
			opts[o.ID] = true
		}
	}
	if t.fn == nil {
		return "", fmt.Errorf("ask_user: no interactive host is available")
	}
	answers, err := t.fn(ctx, in.Questions)
	if err != nil {
		return "", fmt.Errorf("ask_user: %w", err)
	}
	if len(answers) != len(in.Questions) {
		return "", fmt.Errorf("ask_user: host returned incomplete answers")
	}
	for i, a := range answers {
		q := in.Questions[i]
		if a.ID != q.ID {
			return "", fmt.Errorf("ask_user: answer order/id mismatch")
		}
		allowed := map[string]bool{}
		for _, o := range q.Options {
			allowed[o.ID] = true
		}
		if !q.AllowMultiple && len(a.Selected) > 1 {
			return "", fmt.Errorf("ask_user: multiple answers not allowed")
		}
		for _, id := range a.Selected {
			if !allowed[id] {
				return "", fmt.Errorf("ask_user: unknown selected option")
			}
		}
		if len(a.Text) > 4000 {
			return "", fmt.Errorf("ask_user: answer text too large")
		}
	}
	raw, _ := json.Marshal(map[string]any{"answers": answers})
	return string(raw), nil
}
func (askUserTool) Sequential() bool { return true }
