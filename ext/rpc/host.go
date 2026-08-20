package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/subosito/mow"
	iproc "github.com/subosito/mow/internal/proc"
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
	addExtraRootMetadata(out, s.Engine)
	addProcMetadata(out, s.Engine)
	return out
}

func addProcMetadata(out map[string]any, eng *mow.Engine) {
	rows := make([]map[string]any, 0)
	if eng != nil {
		list, err := mow.ProcList(mow.ProcStoreDir(mow.Home(), eng.Workspace()))
		if err == nil {
			for _, p := range list {
				rows = append(rows, map[string]any{
					"id":    p.ID,
					"pid":   p.PID,
					"alive": p.Alive,
					"log":   p.Log,
				})
			}
		}
	}
	out["procs"] = rows
}

func extraRootResult(eng *mow.Engine) []map[string]any {
	rows := make([]map[string]any, 0)
	if eng == nil {
		return rows
	}
	for _, path := range eng.ExtraRoots() {
		rows = append(rows, map[string]any{"path": path, "read_only": false})
	}
	for _, path := range eng.ExtraRootsReadOnly() {
		rows = append(rows, map[string]any{"path": path, "read_only": true})
	}
	return rows
}

func addExtraRootMetadata(out map[string]any, eng *mow.Engine) {
	out["extra_roots"] = extraRootResult(eng)
	if eng == nil {
		out["extra_roots_rw"] = 0
		out["extra_roots_ro"] = 0
		return
	}
	out["extra_roots_rw"] = len(eng.ExtraRoots())
	out["extra_roots_ro"] = len(eng.ExtraRootsReadOnly())
}

func (s *Server) handleSessions(req request) {
	infos, err := s.Engine.Sessions()
	if err != nil {
		s.replyErrTo(req, codeInternalError, err.Error())
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
	s.replyTo(req, map[string]any{"sessions": list})
}

func (s *Server) handleTranscript(req request) {
	msgs := s.Engine.Transcript()
	list := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		content := m.Content
		if utf8.RuneCountInString(content) > maxTranscriptRunes {
			content = trimRunes(content, maxTranscriptRunes) + "…"
		}
		row := map[string]any{"role": m.Role, "content": content}
		if !m.Timestamp.IsZero() {
			row["ts"] = m.Timestamp.UTC().Format(time.RFC3339)
		}
		list = append(list, row)
	}
	s.replyTo(req, map[string]any{"messages": list})
}

func (s *Server) handleSteer(req request) {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if strings.TrimSpace(p.Text) == "" {
		s.replyErrTo(req, codeInvalidRequest, "steer requires params.text")
		return
	}
	if utf8.RuneCountInString(p.Text) > maxPromptTextRunes {
		s.replyErrTo(req, codeInvalidRequest, "steer text too long")
		return
	}
	s.Engine.Steer(p.Text)
	s.replyTo(req, map[string]any{"ok": true})
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
	s.replyTo(req, map[string]any{"commands": list})
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
		s.replyErrTo(req, codeInvalidRequest, "slash requires params.name")
		return
	}
	cmd, ok := slash.Lookup(token)
	if !ok {
		s.replyErrTo(req, codeMethodNotFound, "unknown slash command "+token)
		return
	}
	if slash.IsHelpArgs(p.Args) {
		s.replyTo(req, map[string]any{"title": "/" + cmd.Name, "body": cmd.Usage})
		return
	}
	if cmd.Exclusive && s.Engine.Status().Busy {
		s.replyErrTo(req, codeInvalidRequest, "exclusive slash command cannot run while a turn is in flight")
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
	// Run errors are user-level (bad flags, empty scope) — the external TUI
	// paints them as an error entry, not a transport failure.
	if err != nil {
		s.replyTo(req, map[string]any{
			"title": res.Title,
			"body":  res.Body,
			"error": err.Error(),
		})
		return
	}
	s.replyTo(req, map[string]any{"title": res.Title, "body": res.Body})
}

func (s *Server) handleModelList(ctx context.Context, req request) {
	if ctx == nil {
		ctx = context.Background()
	}
	infos, err := s.Engine.ListModels(ctx)
	if err != nil {
		s.replyErrTo(req, codeInternalError, err.Error())
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
	s.replyTo(req, map[string]any{"models": list, "current": cur})
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
		s.replyErrTo(req, codeInvalidRequest, "model.set requires params.id")
		return
	}
	if err := s.Engine.SetModel(id); err != nil {
		s.replyErrTo(req, codeInternalError, err.Error())
		return
	}
	// Echo the effort the engine landed on after SetModel aligned it to the
	// new model's catalog default_effort (unpinning any leftover tier from a
	// previous model). Hosts like mowi show `model (effort)` from this.
	s.replyTo(req, map[string]any{"ok": true, "model": s.Engine.Model(), "effort": s.Engine.DisplayEffort()})
}

func (s *Server) handleEffortList(req request) {
	cur := s.Engine.DisplayEffort()
	efforts := s.Engine.Efforts()
	list := make([]map[string]any, 0, len(efforts))
	for _, e := range efforts {
		list = append(list, map[string]any{
			"id":      e,
			"current": strings.EqualFold(e, cur),
		})
	}
	s.replyTo(req, map[string]any{
		"efforts": list,
		"current": cur,
		"default": s.Engine.DefaultEffort(),
	})
}

func (s *Server) handleEffortSet(req request) {
	var p struct {
		ID     string `json:"id"`
		Effort string `json:"effort"`
	}
	_ = json.Unmarshal(req.Params, &p)
	id := strings.TrimSpace(p.ID)
	if id == "" {
		id = strings.TrimSpace(p.Effort)
	}
	if id == "" {
		s.replyErrTo(req, codeInvalidRequest, "effort.set requires params.id")
		return
	}
	if err := s.Engine.SetEffort(id); err != nil {
		s.replyErrTo(req, codeInvalidRequest, err.Error())
		return
	}
	s.replyTo(req, map[string]any{"ok": true, "effort": s.Engine.DisplayEffort()})
}

// handleContext reports context-window usage so a UI can paint a gauge
// without replaying the transcript itself.
func (s *Server) handleContext(req request) {
	used := s.Engine.ContextTokens()
	lim := s.Engine.Limits()
	// Compact estimates can leak a raw char count into lastProviderTokens when
	// chars/token calibration is 1.0. The header chip is tokens / window;
	// a char count against a 500k tok-eq cap reads as 271%.
	if lim.ContextWindow > 0 && used > lim.ContextWindow {
		used = (used + 2) / 4
		if used > lim.ContextWindow {
			used = lim.ContextWindow
		}
	}
	out := map[string]any{"tokens": used}
	if lim.ContextWindow > 0 {
		out["context_window"] = lim.ContextWindow
		out["remaining"] = max(lim.ContextWindow-used, 0)
		out["percent"] = float64(used) / float64(lim.ContextWindow) * 100
	}
	if lim.InputPrice > 0 {
		out["input_price"] = lim.InputPrice
	}
	if lim.OutputPrice > 0 {
		out["output_price"] = lim.OutputPrice
	}
	s.replyTo(req, out)
}

// handleCompact shrinks the transcript in place. maxChars <= 0 lets the
// engine pick its own budget.
func (s *Server) handleCompact(req request) {
	var p struct {
		MaxChars int `json:"max_chars"`
	}
	_ = json.Unmarshal(req.Params, &p)
	rep, err := s.Engine.Compact(p.MaxChars)
	if err != nil {
		s.replyErrTo(req, codeInvalidRequest, err.Error())
		return
	}
	s.replyTo(req, map[string]any{
		"layer":           rep.Layer,
		"chars_before":    rep.CharsBefore,
		"chars_after":     rep.CharsAfter,
		"chars_saved":     rep.CharsSaved,
		"messages_before": rep.MessagesBefore,
		"messages_after":  rep.MessagesAfter,
		"over_budget":     rep.OverBudget,
		"tokens":          s.Engine.ContextTokens(),
	})
}

// handleRewind drops the last exchange and hands the user text back so a UI
// can refill its input box for an edit-and-resend.
func (s *Server) handleRewind(req request) {
	last, ok := s.Engine.Rewind()
	s.replyTo(req, map[string]any{"ok": ok, "last_user": last})
}

func (s *Server) handleSkillList(req request) {
	infos := s.Engine.AvailableSkillInfos()
	names := make([]string, 0, len(infos))
	detail := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Folder)
		row := map[string]any{
			"id":     info.Folder,
			"name":   info.Name,
			"folder": info.Folder,
		}
		if info.Description != "" {
			row["description"] = info.Description
		}
		if info.DisableModelInvocation {
			row["disable_model_invocation"] = true
		}
		detail = append(detail, row)
	}
	s.replyTo(req, map[string]any{
		"skills": names, // frozen string list for older hosts
		"items":  detail,
	})
}

func (s *Server) handleSkillActivate(req request) {
	var p struct {
		Names []string `json:"names"`
		Name  string   `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &p)
	names := p.Names
	if len(names) == 0 && strings.TrimSpace(p.Name) != "" {
		names = []string{p.Name}
	}
	if len(names) == 0 {
		s.replyErrTo(req, codeInvalidRequest, "skill.activate requires params.names")
		return
	}
	activated, unknown := s.Engine.ActivateSkills(names...)
	s.replyTo(req, map[string]any{
		"activated": append([]string{}, activated...),
		"unknown":   append([]string{}, unknown...),
	})
}

func (s *Server) handlePluginList(req request) {
	plugins := s.Engine.AvailablePlugins()
	items := make([]map[string]any, 0, len(plugins))
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, p.ID)
		row := map[string]any{
			"id":      p.ID,
			"name":    p.Name,
			"version": p.Version,
		}
		if p.Description != "" {
			row["description"] = p.Description
		}
		if len(p.SkillFolders) > 0 {
			row["skills"] = append([]string{}, p.SkillFolders...)
		}
		if p.Always {
			row["always"] = true
		}
		items = append(items, row)
	}
	s.replyTo(req, map[string]any{
		"plugins": names,
		"items":   items,
	})
}

func (s *Server) handleProcList(req request) {
	dir := iproc.StoreDir(mow.Home(), s.Engine.Workspace())
	list, err := iproc.List(dir)
	if err != nil {
		s.replyErrTo(req, codeInternalError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, p := range list {
		items = append(items, map[string]any{
			"id":    p.ID,
			"pid":   p.PID,
			"log":   p.Log,
			"alive": p.Alive,
		})
	}
	s.replyTo(req, map[string]any{"items": items})
}
