package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow/slash"
)

// maxTranscriptRunes caps each transcript message so resuming a long session
// cannot put megabytes on a single response line.
const maxTranscriptRunes = 32 << 10 // 32k runes

// statusResult is Engine.Status plus control-plane state the UI owns
// (permission mode, outstanding prompts). Existing fields — including "busy" —
// keep their names and types.
func (s *Server) statusResult() map[string]any {
	st := s.Engine.Status()
	out := map[string]any{
		"busy":        st.Busy,
		"allow_write": st.AllowWrite,
		"allow_shell": st.AllowShell,
		"ask_mode":    s.AskMode(),
	}
	if st.RunID != "" {
		out["run_id"] = st.RunID
	}
	if st.SessionID != "" {
		out["session_id"] = st.SessionID
	}
	if st.Workspace != "" {
		out["workspace"] = st.Workspace
	}
	if st.Model != "" {
		out["model"] = st.Model
	}
	if st.Wire != "" {
		out["wire"] = st.Wire
	}
	out["pending_perm"] = s.pendingCount()
	return out
}

func (s *Server) handleSessions(req request) {
	infos, err := s.Engine.Sessions()
	if err != nil {
		s.replyErr(req.ID, codeInternalError, err.Error())
		return
	}
	list := make([]map[string]any, 0, len(infos))
	for _, in := range infos {
		list = append(list, map[string]any{
			"id":      in.ID,
			"updated": in.Updated,
			"preview": trimRunes(in.Preview, maxTranscriptRunes),
		})
	}
	s.reply(req.ID, map[string]any{"sessions": list})
}

func (s *Server) handleTranscript(req request) {
	msgs := s.Engine.Transcript()
	list := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		content := m.Content
		if utf8.RuneCountInString(content) > maxTranscriptRunes {
			content = trimRunes(content, maxTranscriptRunes) + "…"
		}
		list = append(list, map[string]any{"role": m.Role, "content": content})
	}
	s.reply(req.ID, map[string]any{"messages": list})
}

func (s *Server) handleSteer(req request) {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if strings.TrimSpace(p.Text) == "" {
		s.replyErr(req.ID, codeInvalidRequest, "steer requires params.text")
		return
	}
	if utf8.RuneCountInString(p.Text) > maxPromptTextRunes {
		s.replyErr(req.ID, codeInvalidRequest, "steer text too long")
		return
	}
	s.Engine.Steer(p.Text)
	s.reply(req.ID, map[string]any{"ok": true})
}

// handleSlashList reports the slash commands linked into this binary. Usage is
// omitted on purpose (it can be pages long); fetch it with slash + help args.
func (s *Server) handleSlashList(req request) {
	cmds := slash.Commands()
	list := make([]map[string]any, 0, len(cmds))
	for _, c := range cmds {
		list = append(list, map[string]any{
			"name":      c.Name,
			"summary":   c.Summary,
			"exclusive": c.Exclusive,
			"aliases":   append([]string{}, c.Aliases...),
		})
	}
	s.reply(req.ID, map[string]any{"commands": list})
}

func (s *Server) handleSlash(ctx context.Context, req request) {
	var p struct {
		Name  string   `json:"name"`
		Args  []string `json:"args"`
		Color bool     `json:"color"`
	}
	_ = json.Unmarshal(req.Params, &p)
	token := strings.TrimSpace(p.Name)
	if token == "" {
		s.replyErr(req.ID, codeInvalidRequest, "slash requires params.name")
		return
	}
	cmd, ok := slash.Lookup(token)
	if !ok {
		s.replyErr(req.ID, codeMethodNotFound, "unknown slash command "+token)
		return
	}
	if slash.IsHelpArgs(p.Args) {
		s.reply(req.ID, map[string]any{"title": "/" + cmd.Name, "body": cmd.Usage})
		return
	}
	if cmd.Exclusive && s.Engine.Status().Busy {
		s.replyErr(req.ID, codeInvalidRequest, "exclusive slash command cannot run while a turn is in flight")
		return
	}
	res, err := cmd.Run(ctx, slash.Request{
		Name:      cmd.Name,
		Invoked:   strings.TrimPrefix(token, "/"),
		Args:      p.Args,
		Engine:    s.Engine,
		Workspace: s.Engine.Workspace(),
		Color:     p.Color,
	})
	// Run errors are user-level (bad flags, empty scope) — same as mowi
	// painting an error entry, not a transport failure.
	if err != nil {
		s.reply(req.ID, map[string]any{
			"title": res.Title,
			"body":  res.Body,
			"error": err.Error(),
		})
		return
	}
	s.reply(req.ID, map[string]any{"title": res.Title, "body": res.Body})
}


func (s *Server) handleModelList(ctx context.Context, req request) {
	if ctx == nil {
		ctx = context.Background()
	}
	infos, err := s.Engine.ListModels(ctx)
	if err != nil {
		s.replyErr(req.ID, codeInternalError, err.Error())
		return
	}
	cur := s.Engine.Model()
	list := make([]map[string]any, 0, len(infos))
	for _, m := range infos {
		row := map[string]any{
			"id":      m.ID,
			"current": strings.EqualFold(m.ID, cur),
		}
		if m.Wire != "" {
			row["wire"] = m.Wire
		}
		list = append(list, row)
	}
	s.reply(req.ID, map[string]any{"models": list, "current": cur})
}

func (s *Server) handleModelSet(req request) {
	var p struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &p)
	id := strings.TrimSpace(p.ID)
	if id == "" {
		id = strings.TrimSpace(p.Name)
	}
	if id == "" {
		s.replyErr(req.ID, codeInvalidRequest, "model.set requires params.id")
		return
	}
	if err := s.Engine.SetModel(id); err != nil {
		s.replyErr(req.ID, codeInternalError, err.Error())
		return
	}
	s.reply(req.ID, map[string]any{"ok": true, "model": s.Engine.Model()})
}
