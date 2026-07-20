package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/subosito/mow/ext"
)

// Active finish signal for the in-flight goal step (set via tool goal_report).
type finishSignal struct {
	mu      sync.Mutex
	done    bool
	failed  bool
	reason  string
	summary string
}

func (f *finishSignal) report(status, reason, summary string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summary = strings.TrimSpace(summary)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "complete", "success":
		f.done = true
		f.failed = false
		f.reason = ""
	case "failed", "fail", "blocked", "error":
		f.failed = true
		f.done = false
		f.reason = strings.TrimSpace(reason)
	}
}

func (f *finishSignal) outcome() (done, failed bool, reason, summary string) {
	if f == nil {
		return false, false, "", ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done, f.failed, f.reason, f.summary
}

type finishCtxKey struct{}

func withFinish(ctx context.Context, f *finishSignal) context.Context {
	return context.WithValue(ctx, finishCtxKey{}, f)
}

func finishFrom(ctx context.Context) *finishSignal {
	v, _ := ctx.Value(finishCtxKey{}).(*finishSignal)
	return v
}

func init() {
	ext.RegisterTool(&reportTool{})
}

// reportTool lets the model declare goal completion without fragile text markers.
// Only active while a goal.Runner step is in progress (context carries the signal).
type reportTool struct{}

func (reportTool) Name() string { return "goal_report" }
func (reportTool) Description() string {
	return "Report outer-loop goal completion. Call when the goal is fully done or blocked. " +
		"Args: status (done|failed), summary (required when done: the user-facing result, e.g. bullets), " +
		"reason (optional, for failed)."
}
func (reportTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["done","failed"]},"summary":{"type":"string","description":"User-facing result when status=done"},"reason":{"type":"string"}},"required":["status"]}`)
}
func (reportTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	sig := finishFrom(ctx)
	if sig == nil {
		return "goal_report ignored (no active outer-loop goal)", nil
	}
	sig.report(a.Status, a.Reason, a.Summary)
	if strings.EqualFold(a.Status, "done") {
		if strings.TrimSpace(a.Summary) == "" {
			return "recorded: goal done (warning: pass summary= with the result text for the user)", nil
		}
		return "recorded: goal done", nil
	}
	return fmt.Sprintf("recorded: goal failed (%s)", strings.TrimSpace(a.Reason)), nil
}

// ParseStatusJSON extracts ```goal-status {json} ``` blocks from assistant text.
func ParseStatusJSON(text string) (done, failed bool, reason, summary string) {
	const fence = "```goal-status"
	for {
		i := strings.Index(text, fence)
		if i < 0 {
			return
		}
		rest := text[i+len(fence):]
		rest = strings.TrimLeft(rest, " \t\r\n")
		end := strings.Index(rest, "```")
		if end < 0 {
			return
		}
		body := strings.TrimSpace(rest[:end])
		var obj struct {
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(body), &obj) == nil {
			switch strings.ToLower(strings.TrimSpace(obj.Status)) {
			case "done", "complete", "success":
				return true, false, "", strings.TrimSpace(obj.Summary)
			case "failed", "fail", "blocked", "error":
				return false, true, strings.TrimSpace(obj.Reason), strings.TrimSpace(obj.Summary)
			}
		}
		text = rest[end+3:]
	}
}
