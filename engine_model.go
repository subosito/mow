package mow

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow/internal/llm"
)

// Model returns the active chat model id.
func (e *Engine) Model() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil && e.client.Model != "" {
		return e.client.Model
	}
	if e.cfg != nil {
		return e.cfg.LLM.Model
	}
	return ""
}

// SetModel switches the chat model for subsequent Prompt calls.
// No-op-safe: returns an error when using a custom Options.Chat inject (no live client).
func (e *Engine) SetModel(id string) error {
	if e == nil {
		return fmt.Errorf("mow: nil engine")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("mow: empty model id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		// Custom providers may opt in via the ModelSwitcher extension.
		if sw, ok := e.provider.(ModelSwitcher); ok {
			if err := sw.SetModel(id); err != nil {
				return err
			}
			if e.cfg != nil {
				e.cfg.LLM.Model = id
			}
			return nil
		}
		return fmt.Errorf("mow: model switch requires the built-in client or a Provider implementing ModelSwitcher")
	}
	e.client.Model = id
	if e.cfg != nil {
		e.cfg.LLM.Model = id
	}
	return nil
}

// SetWire switches the client wire (openai-chat-completions | anthropic-messages).
func (e *Engine) SetWire(wire string) error {
	if e == nil {
		return fmt.Errorf("mow: nil engine")
	}
	wire = llm.NormalizeWire(wire)
	if !llm.IsKnownChatWire(wire) {
		return fmt.Errorf("mow: unsupported wire %q (want openai-chat-completions or anthropic-messages)", wire)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return fmt.Errorf("mow: wire switch requires live LLM client")
	}
	e.client.Wire = wire
	if e.cfg != nil {
		e.cfg.LLM.Wire = wire
	}
	return nil
}

// Wire returns the active client wire id.
func (e *Engine) Wire() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil && e.client.Wire != "" {
		return e.client.Wire
	}
	if e.cfg != nil {
		return e.cfg.LLM.Wire
	}
	return llm.WireOpenAIChat
}

// ModelInfo is a listed model (id + optional preferred wire metadata).
type ModelInfo struct {
	ID    string
	Wire  string
	Wires []string
}

// ListModels returns available models from GET /models.
// With a custom Chat inject and no live client, returns the current model alone when known.
func (e *Engine) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if e == nil {
		return nil, fmt.Errorf("mow: nil engine")
	}
	e.mu.Lock()
	client := e.client
	current := ""
	if client != nil {
		current = client.Model
	} else if e.cfg != nil {
		current = e.cfg.LLM.Model
	}
	e.mu.Unlock()

	if client == nil {
		// Custom providers may opt in via the ModelLister extension.
		if ml, ok := e.provider.(ModelLister); ok {
			return ml.ListModels(ctx)
		}
		if current != "" {
			return []ModelInfo{{ID: current}}, nil
		}
		return []ModelInfo{}, nil
	}
	infos, err := client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(infos))
	for _, m := range infos {
		out = append(out, ModelInfo{ID: m.ID, Wire: m.Wire, Wires: m.Wires})
	}
	return out, nil
}

// SetModelWithWire sets model and, when wire is a known chat wire, switches client wire too.
// Used when GET /models returns preferred wire metadata.
func (e *Engine) SetModelWithWire(id, wire string) error {
	if err := e.SetModel(id); err != nil {
		return err
	}
	wire = strings.TrimSpace(wire)
	if wire == "" {
		return nil
	}
	if !llm.IsKnownChatWire(wire) {
		// Catalog may advertise media-only wires; keep current chat wire.
		return nil
	}
	return e.SetWire(wire)
}
