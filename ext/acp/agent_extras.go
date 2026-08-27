package acp

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow"
	"github.com/subosito/mow/slash"
)

const maxTranscriptRunes = 32 << 10

// extraCapabilities is advertised under agentCapabilities.experimental so a
// power client (mowi) can feature-detect without a second protocol. Generic
// ACP clients ignore unknown capability keys.
func extraCapabilities() map[string]any {
	return map[string]any{
		"steer":      true,
		"compact":    true,
		"rewind":     true,
		"skill":      true,
		"plugin":     true,
		"transcript": true,
		"status":     true,
		"context":    true,
		"proc":       true,
		"ping":       true,
		"slash":      true,
	}
}

func extraMethodNames() []string {
	return []string{
		"steer", "compact", "rewind",
		"skill.list", "skill.activate", "plugin.list",
		"transcript", "status", "context", "proc.list",
		"ping", "slash",
	}
}

// handleExtra serves optional unprefixed methods on the same ACP connection.
// Returns false when req.Method is not an extra (caller emits -32601).
func (a *agentServer) handleExtra(parent context.Context, req request) bool {
	switch req.Method {
	case "steer":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if strings.TrimSpace(p.Text) == "" {
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: errInvalid, Message: "steer requires params.text"},
			})
			return true
		}
		a.eng.Steer(p.Text)
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
		return true
	case "compact":
		var p struct {
			MaxChars int `json:"max_chars"`
		}
		_ = json.Unmarshal(req.Params, &p)
		rep, err := a.eng.Compact(p.MaxChars)
		if err != nil {
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: errInvalid, Message: err.Error()},
			})
			return true
		}
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"layer":           rep.Layer,
				"chars_before":    rep.CharsBefore,
				"chars_after":     rep.CharsAfter,
				"chars_saved":     rep.CharsSaved,
				"messages_before": rep.MessagesBefore,
				"messages_after":  rep.MessagesAfter,
				"over_budget":     rep.OverBudget,
				"tokens":          a.eng.ContextTokens(),
			},
		})
		return true
	case "rewind":
		last, ok := a.eng.Rewind()
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"ok": ok, "last_user": last},
		})
		return true
	case "skill.list":
		infos := a.eng.AvailableSkillInfos()
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
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"skills": names, "items": detail},
		})
		return true
	case "skill.activate":
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
			a.write(response{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: errInvalid, Message: "skill.activate requires params.names"},
			})
			return true
		}
		activated, unknown := a.eng.ActivateSkills(names...)
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"activated": append([]string{}, activated...),
				"unknown":   append([]string{}, unknown...),
			},
		})
		return true
	case "plugin.list":
		plugins := a.eng.AvailablePlugins()
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
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"plugins": names, "items": items},
		})
		return true
	case "transcript":
		turns := a.eng.Transcript()
		msgs := make([]map[string]any, 0, len(turns))
		for _, m := range turns {
			role := strings.ToLower(strings.TrimSpace(m.Role))
			if role != "user" && role != "assistant" {
				continue
			}
			content := m.Content
			if utf8.RuneCountInString(content) > maxTranscriptRunes {
				content = string([]rune(content)[:maxTranscriptRunes])
			}
			row := map[string]any{"role": role, "content": content}
			if !m.Timestamp.IsZero() {
				row["ts"] = m.Timestamp.UTC().Format("2006-01-02T15:04:05Z")
			}
			msgs = append(msgs, row)
		}
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"messages": msgs},
		})
		return true
	case "status":
		st := a.eng.Status()
		out := map[string]any{
			"busy":               st.Busy,
			"allow_write":        st.AllowWrite,
			"allow_shell":        st.AllowShell,
			"ask_mode":           a.askMode(),
			"mode":               a.sessionMode(),
			"pending_perm":       len(a.pending),
			"pending_permission": len(a.pending),
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
		out["extra_roots"] = extraRootRows(a.eng)
		out["extra_roots_rw"] = len(a.eng.ExtraRoots())
		out["extra_roots_ro"] = len(a.eng.ExtraRootsReadOnly())
		out["procs"] = procRows(a.eng)
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: out})
		return true
	case "context":
		used := a.eng.ContextTokens()
		lim := a.eng.Limits()
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
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: out})
		return true
	case "proc.list":
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"items": procRows(a.eng)},
		})
		return true
	case "ping":
		a.write(response{JSONRPC: "2.0", ID: req.ID, Result: "pong"})
		return true
	case "slash":
		a.handleSlashExtra(parent, req)
		return true
	default:
		return false
	}
}

func (a *agentServer) askMode() bool {
	return a.sessionMode() == ModeAsk
}

func (a *agentServer) sessionMode() string {
	if a == nil {
		return ModeCode
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sid := a.activeSID
	if sid == "" {
		for id := range a.sessions {
			sid = id
			break
		}
	}
	if s := a.sessions[sid]; s != nil && s.mode != "" {
		return s.mode
	}
	return ModeCode
}

func (a *agentServer) handleSlashExtra(ctx context.Context, req request) {
	var p struct {
		Name  string   `json:"name"`
		Args  []string `json:"args"`
		Color bool     `json:"color"`
	}
	_ = json.Unmarshal(req.Params, &p)
	token := strings.TrimSpace(p.Name)
	if token == "" {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errInvalid, Message: "slash requires params.name"},
		})
		return
	}
	cmd, ok := slash.Lookup(token)
	if !ok {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errMethod, Message: "unknown slash command " + token},
		})
		return
	}
	if slash.IsHelpArgs(p.Args) {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"title": "/" + cmd.Name, "body": cmd.Usage},
		})
		return
	}
	if cmd.Exclusive && a.eng.Status().Busy {
		a.write(response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: errInvalid, Message: "exclusive slash command cannot run while a turn is in flight"},
		})
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := cmd.Run(ctx, slash.Request{
		Name:      cmd.Name,
		Invoked:   strings.TrimPrefix(token, "/"),
		Args:      p.Args,
		Engine:    a.eng,
		Workspace: a.eng.Workspace(),
		Color:     p.Color,
	})
	out := map[string]any{"title": res.Title, "body": res.Body}
	if err != nil {
		out["error"] = err.Error()
	}
	a.write(response{JSONRPC: "2.0", ID: req.ID, Result: out})
}

func extraRootRows(eng *mow.Engine) []map[string]any {
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

func procRows(eng *mow.Engine) []map[string]any {
	rows := make([]map[string]any, 0)
	if eng == nil {
		return rows
	}
	list, err := mow.ProcList(mow.ProcStoreDir(mow.Home(), eng.Workspace()))
	if err != nil {
		return rows
	}
	for _, p := range list {
		rows = append(rows, map[string]any{
			"id":    p.ID,
			"pid":   p.PID,
			"alive": p.Alive,
			"log":   p.Log,
		})
	}
	return rows
}
