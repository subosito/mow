package agent

import "context"

// beforeFinish runs the before-finish hooks for a final answer and returns
// the text to inject when the Run should continue. Non-empty texts from
// several hooks are concatenated in order. Once MaxFinishContinuations
// consecutive continuations were granted, hooks are not consulted again so
// the answer is returned.
func beforeFinish(ctx context.Context, hooks []BeforeFinishFunc, text string, done int) (string, error) {
	if len(hooks) == 0 || done >= MaxFinishContinuations {
		return "", nil
	}
	var out string
	for _, h := range hooks {
		if h == nil {
			continue
		}
		d, err := h(ctx, BeforeFinishEvent{AssistantText: text, Continuations: done})
		if err != nil {
			return "", err
		}
		if d.Continue == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += d.Continue
	}
	return out, nil
}
