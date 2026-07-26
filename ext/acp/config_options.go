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
	configIDModel = "model"
)

// modelConfigMax is the max catalog entries advertised in the picker.
// Large catalogs (hundreds of ids) are truncated for UX; current model is always kept.
const modelConfigMax = 80

// sessionConfigOptions builds ACP configOptions for session/new|load|resume.
// Currently: model selector (category "model") from Engine.ListModels + current model.
func (a *agentServer) sessionConfigOptions(ctx context.Context) []map[string]any {
	if a == nil || a.eng == nil {
		return nil
	}
	opt := a.modelConfigOption(ctx)
	if opt == nil {
		return nil
	}
	return []map[string]any{opt}
}

func (a *agentServer) modelConfigOption(ctx context.Context) map[string]any {
	eng := a.eng
	cur := strings.TrimSpace(eng.Model())
	list, err := a.listModels(ctx)
	if err != nil {
		// Catalog down: still expose current model so the UI shows something switchable later.
		if cur == "" {
			return nil
		}
		list = []mow.ModelInfo{{ID: cur, Wire: eng.Wire()}}
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
		name := id
		desc := ""
		if w := strings.TrimSpace(m.Wire); w != "" {
			name = id + "  [" + w + "]"
			desc = "wire " + w
		}
		options = append(options, map[string]any{
			"value":       id,
			"name":        name,
			"description": desc,
		})
	}
	if len(options) == 0 {
		return nil
	}
	return map[string]any{
		"id":           configIDModel,
		"name":         "Model",
		"description":  "Chat model for this session (catalog wire applied when known)",
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

// applyModelConfig sets the engine model (+ catalog wire when known).
func (a *agentServer) applyModelConfig(ctx context.Context, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("model value required")
	}
	// Strip accidental "id  [wire]" paste from display labels.
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
