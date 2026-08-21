package acp

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/subosito/mow"
)

// fileMutation is one peer write/edit observed on session/update.
type fileMutation struct {
	CallID string
	Tool   string // write | edit
	Path   string
	Diff   string // unified diff when the peer sent old/new or a diff block
	Done   bool
	Failed bool
}

func isTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "success", "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func isFailedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func fileMutationTool(kind, title string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "write", "create", "overwrite", "file_write", "file_create":
		return "write"
	case "edit", "patch", "file_edit", "str_replace", "apply_patch":
		return "edit"
	}
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case strings.HasPrefix(t, "write ") || strings.HasPrefix(t, "writing ") || strings.HasPrefix(t, "wrote "):
		return "write"
	case strings.HasPrefix(t, "edit ") || strings.HasPrefix(t, "editing ") || strings.HasPrefix(t, "edited ") || strings.HasPrefix(t, "patch ") || strings.Contains(t, "strreplace") || strings.Contains(t, "str_replace"):
		return "edit"
	}
	return ""
}

func isPathish(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	return strings.ContainsAny(s, "/.\\")
}

func rawStringField(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func peerMutationPath(u sessionUpdate) string {
	for _, loc := range u.Locations {
		if p := strings.TrimSpace(loc.Path); p != "" {
			return p
		}
	}
	if p := rawStringField(u.RawInput, "path", "file"); p != "" {
		return p
	}
	title := strings.TrimSpace(u.Title)
	if isPathish(title) {
		return title
	}
	fields := strings.Fields(title)
	if n := len(fields); n > 0 && isPathish(fields[n-1]) {
		return fields[n-1]
	}
	return ""
}

func peerMutationDiff(u sessionUpdate) string {
	for _, raw := range u.ToolContent {
		if d := diffFromContentBlock(raw); d != "" {
			return d
		}
	}
	if u.Content != nil && looksUnifiedDiff(u.Content.Text) {
		return strings.TrimSpace(u.Content.Text)
	}
	if d := diffFromContentBlock(u.RawOutput); d != "" {
		return d
	}
	return ""
}

func diffFromContentBlock(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '[' {
		var blocks []json.RawMessage
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		for _, b := range blocks {
			if d := diffFromContentBlock(b); d != "" {
				return d
			}
		}
		return ""
	}
	var block struct {
		Type     string `json:"type"`
		Path     string `json:"path"`
		OldText  string `json:"oldText"`
		NewText  string `json:"newText"`
		Old_Text string `json:"old_text"`
		New_Text string `json:"new_text"`
		Diff     string `json:"diff"`
		Text     string `json:"text"`
		Content  *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &block) != nil {
		return ""
	}
	oldText := firstNonEmpty(block.OldText, block.Old_Text)
	newText := firstNonEmpty(block.NewText, block.New_Text)
	switch strings.ToLower(strings.TrimSpace(block.Type)) {
	case "diff":
		if d := unifiedFromTexts(block.Path, oldText, newText); d != "" {
			return d
		}
		if looksUnifiedDiff(block.Diff) {
			return strings.TrimSpace(block.Diff)
		}
		if looksUnifiedDiff(block.Text) {
			return strings.TrimSpace(block.Text)
		}
	case "content", "text", "":
		if block.Content != nil && looksUnifiedDiff(block.Content.Text) {
			return strings.TrimSpace(block.Content.Text)
		}
		if looksUnifiedDiff(block.Text) {
			return strings.TrimSpace(block.Text)
		}
		if looksUnifiedDiff(block.Diff) {
			return strings.TrimSpace(block.Diff)
		}
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func looksUnifiedDiff(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	hasHunk, hasBody := false, false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "@@") {
			hasHunk = true
		} else if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			if !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				hasBody = true
			}
		}
	}
	return hasHunk && hasBody
}

func unifiedFromTexts(path, oldText, newText string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "file"
	}
	oldText = strings.TrimRight(oldText, "\n")
	newText = strings.TrimRight(newText, "\n")
	if oldText == "" && newText == "" {
		return ""
	}
	oldLines := splitKeep(oldText)
	newLines := splitKeep(newText)
	var b strings.Builder
	b.WriteString("--- ")
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString("+++ ")
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString("@@ -1,")
	b.WriteString(strconv.Itoa(len(oldLines)))
	b.WriteString(" +1,")
	b.WriteString(strconv.Itoa(len(newLines)))
	b.WriteString(" @@\n")
	const capLines = 80
	n := 0
	for _, line := range oldLines {
		if n >= capLines {
			b.WriteString("… (diff truncated)\n")
			return b.String()
		}
		b.WriteByte('-')
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	for _, line := range newLines {
		if n >= capLines {
			b.WriteString("… (diff truncated)\n")
			return b.String()
		}
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	return b.String()
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func peerFileMutation(u sessionUpdate) (fileMutation, bool) {
	tool := fileMutationTool(u.Kind, u.Title)
	if tool == "" {
		return fileMutation{}, false
	}
	path := peerMutationPath(u)
	if path == "" {
		return fileMutation{}, false
	}
	id := strings.TrimSpace(u.ToolCallID)
	if id == "" {
		id = tool + ":" + path
	}
	return fileMutation{
		CallID: id,
		Tool:   tool,
		Path:   path,
		Diff:   peerMutationDiff(u),
		Done:   isTerminalStatus(u.Status),
		Failed: isFailedStatus(u.Status),
	}, true
}

func emitHostFileMutation(eng *mow.Engine, agent string, m fileMutation, started map[string]bool) {
	if eng == nil || strings.TrimSpace(m.Path) == "" {
		return
	}
	if started == nil {
		started = map[string]bool{}
	}
	args, _ := json.Marshal(map[string]string{"path": m.Path})
	callID := "acp:" + strings.TrimSpace(agent) + ":" + m.CallID
	if !started[m.CallID] {
		started[m.CallID] = true
		eng.Emit(mow.Event{
			Type:       mow.EventToolStart,
			Tool:       m.Tool,
			ToolCallID: callID,
			Args:       args,
			Agent:      agent,
			Path:       m.Path,
		})
	}
	// Cursor-class peers often attach the diff on in_progress and never
	// send a terminal status. A non-empty diff is enough to close the card.
	if !m.Done && m.Diff == "" {
		return
	}
	ev := mow.Event{
		Type:       mow.EventToolEnd,
		Tool:       m.Tool,
		ToolCallID: callID,
		Args:       args,
		Result:     m.Diff,
		Agent:      agent,
		Path:       m.Path,
	}
	if m.Failed {
		ev.Error = "peer tool failed"
	}
	eng.Emit(ev)
}
