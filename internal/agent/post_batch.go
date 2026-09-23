package agent

import (
	"strings"

	"github.com/subosito/mow/internal/llm"
)

func appendPostBatchContext(calls []llm.ToolCall, byName map[string]Tool, out []llm.Message) {
	if len(out) == 0 {
		return
	}
	seen := map[string]bool{}
	var notes []string
	for _, call := range calls {
		tool := byName[call.Function.Name]
		if tool == nil {
			continue
		}
		source, ok := tool.(interface{ PostBatchContext() string })
		if !ok {
			continue
		}
		note := strings.TrimSpace(source.PostBatchContext())
		if note != "" && !seen[note] {
			seen[note] = true
			notes = append(notes, note)
		}
	}
	if len(notes) == 0 {
		return
	}
	out[len(out)-1].Content += "\n\n" + strings.Join(notes, "\n")
}
