package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/subosito/mow"
)

// ACP session config option ids (session/set_config_option configId).
const (
	configIDModel  = "model"
	configIDMode   = "mode"
	configIDEffort = "effort"
)

// modelConfigMax is the max catalog entries advertised in the picker.
// Large catalogs are truncated for UX; current model is always kept.
const modelConfigMax = 80

// sessionConfigOptions builds ACP configOptions for session/new|load|resume.
// Mode + model + effort (thought_level). Effort options come from the catalog
// (GET /models efforts / default_effort) when available — not a static list.
func (a *agentServer) sessionConfigOptions(ctx context.Context, mode string) []map[string]any {
	if a == nil || a.eng == nil {
		return nil
	}
	// Refresh catalog so efforts match the gateway (best-effort).
	_, _ = a.eng.ListModels(ctx)
	out := []map[string]any{modeConfigOption(mode)}
	if opt := a.modelConfigOption(ctx); opt != nil {
		out = append(out, opt)
	}
	if opt := a.effortConfigOption(); opt != nil {
		out = append(out, opt)
	}
	return out
}

// effortConfigOption builds the effort selector from catalog efforts for the
// active model. Returns nil when:
//   - catalog has no efforts for this model (nothing to switch), or
//   - only one effort is advertised (gateway fixed tier; no UI switch).
// Falls back to a static none|low|medium|high list only when the catalog has
// been loaded but the model has no efforts metadata (plain OpenAI/DeepSeek).
func (a *agentServer) effortConfigOption() map[string]any {
	if a == nil || a.eng == nil {
		return nil
	}
	efforts := a.eng.Efforts()
	def := strings.ToLower(strings.TrimSpace(a.eng.DefaultEffort()))
	current := strings.ToLower(strings.TrimSpace(a.eng.Effort()))

	if len(efforts) == 0 {
		// No catalog metadata for this model: generic static list + default.
		return effortConfigOptionStatic(current)
	}
	if len(efforts) == 1 {
		// Single fixed tier — nothing useful to switch; hide the selector.
		return nil
	}

	// Normalize current against catalog.
	if current == "" {
		current = def
	}
	if current == "" || !effortInList(current, efforts) {
		if def != "" && effortInList(def, efforts) {
			current = def
		} else {
			current = strings.ToLower(strings.TrimSpace(efforts[0]))
		}
	}

	options := make([]map[string]any, 0, len(efforts)+1)
	// Leading "default" clears client override so gateway applies default_effort.
	if def != "" {
		options = append(options, map[string]any{
			"value":       "default",
			"name":        "Default (" + def + ")",
			"description": "Gateway default for this model",
		})
	}
	for _, e := range efforts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		options = append(options, map[string]any{
			"value": e,
			"name":  strings.ToUpper(e[:1]) + e[1:],
		})
	}
	if len(options) == 0 {
		return nil
	}
	// UI currentValue: show default pseudo when effort matches default and engine effort is empty.
	uiCurrent := current
	if strings.TrimSpace(a.eng.Effort()) == "" && def != "" {
		uiCurrent = "default"
	}
	return map[string]any{
		"id":           configIDEffort,
		"name":         "Effort",
		"description":  "Reasoning intensity for this model (from gateway catalog)",
		"category":     "thought_level",
		"type":         "select",
		"currentValue": uiCurrent,
		"options":      options,
	}
}

func effortConfigOptionStatic(current string) map[string]any {
	switch current {
	case "none", "low", "medium", "high":
	default:
		current = "default"
	}
	return map[string]any{
		"id":           configIDEffort,
		"name":         "Effort",
		"description":  "Reasoning intensity (provider default if unset)",
		"category":     "thought_level",
		"type":         "select",
		"currentValue": current,
		"options": []map[string]any{
			{"value": "default", "name": "Default", "description": "Provider / model default"},
			{"value": "none", "name": "None"},
			{"value": "low", "name": "Low"},
			{"value": "medium", "name": "Medium"},
			{"value": "high", "name": "High"},
		},
	}
}

func effortInList(effort string, list []string) bool {
	for _, e := range list {
		if strings.EqualFold(strings.TrimSpace(e), effort) {
			return true
		}
	}
	return false
}

func modeConfigOption(current string) map[string]any {
	if current != ModeAsk && current != ModeCode {
		current = ModeCode
	}
	return map[string]any{
		"id":           configIDMode,
		"name":         "Mode",
		"description":  "Ask = read-only tools; Code = full tools allowed by process policy",
		"category":     "mode",
		"type":         "select",
		"currentValue": current,
		"options": []map[string]any{
			{
				"value":       ModeAsk,
				"name":        "Ask",
				"description": "Read-only: no write/edit/bash for this session",
			},
			{
				"value":       ModeCode,
				"name":        "Code",
				"description": "Full tool access allowed by the mow process policy",
			},
		},
	}
}

func (a *agentServer) modelConfigOption(ctx context.Context) map[string]any {
	eng := a.eng
	// Lean id for picker (AG tiers live in effort, not model name).
	cur := strings.TrimSpace(eng.Model())
	list, err := a.listModels(ctx)
	if err != nil {
		// Catalog down: still expose current model so the UI shows something switchable later.
		if cur == "" {
			return nil
		}
		list = []mow.ModelInfo{{ID: cur, Wire: eng.Wire()}}
	} else {
		list = filterChatModels(list)
	}
	if cur == "" && len(list) == 0 {
		return nil
	}
	if cur == "" && len(list) > 0 {
		cur = list[0].ID
	}

	// Ensure current is in the list (exact id match, case-insensitive).
	hasCur := false
	for _, m := range list {
		if strings.EqualFold(m.ID, cur) {
			hasCur = true
			cur = m.ID // canonicalize casing from catalog
			break
		}
	}
	if !hasCur && cur != "" {
		list = append([]mow.ModelInfo{{ID: cur, Wire: eng.Wire()}}, list...)
	}

	if len(list) > modelConfigMax {
		// Prefer keeping current near the front after trim.
		trimmed := make([]mow.ModelInfo, 0, modelConfigMax)
		for _, m := range list {
			if strings.EqualFold(m.ID, cur) {
				trimmed = append(trimmed, m)
				break
			}
		}
		for _, m := range list {
			if len(trimmed) >= modelConfigMax {
				break
			}
			if strings.EqualFold(m.ID, cur) {
				continue
			}
			trimmed = append(trimmed, m)
		}
		list = trimmed
	}

	options := make([]map[string]any, 0, len(list))
	for _, m := range list {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		// Name is id only — wire stays internal (SetModelWithWire still uses catalog metadata).
		options = append(options, map[string]any{
			"value": id,
			"name":  id,
		})
	}
	if len(options) == 0 {
		return nil
	}
	return map[string]any{
		"id":           configIDModel,
		"name":         "Model",
		"description":  "Chat model for this session",
		"category":     "model",
		"type":         "select",
		"currentValue": cur,
		"options":      options,
	}
}

func (a *agentServer) listModels(ctx context.Context) ([]mow.ModelInfo, error) {
	if a == nil || a.eng == nil {
		return nil, fmt.Errorf("nil engine")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return a.eng.ListModels(lctx)
}

// filterChatModels keeps catalog rows suitable for the agent chat picker.
//
// Rules (portable across gateways and plain OpenAI catalogs):
//   - facet set → keep only empty or "chat" (gateway-advertised capability;
//     never parse ":" from the model id — some providers use colon in the id)
//   - no wire metadata → keep (plain catalogs are chat models)
//   - preferred wire set → keep only known chat wires; drop images/speech/…
//
// Wire is never shown in the UI; it is only used for SetModelWithWire.
func filterChatModels(list []mow.ModelInfo) []mow.ModelInfo {
	out := make([]mow.ModelInfo, 0, len(list))
	for _, m := range list {
		if isChatModel(m) {
			out = append(out, m)
		}
	}
	return out
}

func isChatModel(m mow.ModelInfo) bool {
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return false
	}
	// Facet is authoritative when the gateway advertises it. Do not drop by
	// ":" in the id — colon can be part of a legitimate model identifier.
	if f := strings.ToLower(strings.TrimSpace(m.Facet)); f != "" && f != "chat" {
		return false
	}
	w := strings.TrimSpace(m.Wire)
	if w == "" {
		// Plain catalogs (OpenAI, DeepSeek, local servers, …): id only.
		return true
	}
	return isChatWire(w)
}

func isChatWire(w string) bool {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "openai-chat-completions", "openai-responses", "openai-response", "anthropic-messages":
		return true
	default:
		return false
	}
}

// applyModelConfig sets the engine model (+ catalog wire when known).
func (a *agentServer) applyModelConfig(ctx context.Context, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("model value required")
	}
	// Strip accidental "id  [wire]" paste from older labels.
	if i := strings.LastIndex(value, "["); i > 0 && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[:i])
	}
	list, err := a.listModels(ctx)
	wire := ""
	if err == nil {
		for _, m := range list {
			if strings.EqualFold(m.ID, value) {
				value = m.ID
				wire = m.Wire
				break
			}
		}
	}
	if err := a.eng.SetModelWithWire(value, wire); err != nil {
		// Custom Provider may implement ModelSwitcher but not live wire switch.
		if wire != "" {
			if err2 := a.eng.SetModel(value); err2 == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

func (a *agentServer) applyModeConfig(sessionID, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != ModeAsk && mode != ModeCode {
		return fmt.Errorf("modeId must be ask or code")
	}
	sid := strings.TrimSpace(sessionID)
	a.mu.Lock()
	if a.sessions[sid] == nil {
		a.sessions[sid] = &acpSession{mode: mode}
	} else {
		a.sessions[sid].mode = mode
	}
	a.mu.Unlock()
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sid,
			"update": map[string]any{
				"sessionUpdate": "current_mode_update",
				"currentModeId": mode,
			},
		}),
	})
	return nil
}

func (a *agentServer) notifyConfigOptions(sessionID string, opts []map[string]any) {
	if sessionID == "" || len(opts) == 0 {
		return
	}
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "config_option_update",
				"configOptions": opts,
			},
		}),
	})
}
