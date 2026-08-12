package otel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/subosito/mow"
)

func init() {
	// Stock CLI and any host that blank-imports this package get config-driven
	// OTLP when explicitly enabled. Missing/false otel.enabled → no-op.
	mow.SetOTELAuto(autoWire)
}

// autoWire is the mow.OTELAutoFunc registered from init.
func autoWire(eng *mow.Engine, raw map[string]any) error {
	if eng == nil {
		return nil
	}
	cfg := exportConfigFromMap(raw)
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil
	}
	// Engine/config path passes endpoint without an enabled key; explicit
	// enabled: false in YAML/map keeps telemetry off despite an endpoint.
	if v, ok := raw["enabled"]; ok && !boolVal(v) {
		return nil
	}
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exp, err := StartExport(ctx, cfg)
	if err != nil {
		return fmt.Errorf("otel auto-wire: %w", err)
	}
	if exp == nil || exp.Adapter == nil {
		return nil
	}
	eng.AddOnEvent(exp.Adapter.OnEvent)
	eng.RegisterCleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		if err := exp.Shutdown(cctx); err != nil {
			slog.Default().Warn("otel shutdown", "err", err)
		}
	})
	return nil
}

// exportConfigFromMap decodes the engine-facing loose map (from config.OTEL).
func exportConfigFromMap(raw map[string]any) ExportConfig {
	if raw == nil {
		return ExportConfig{}
	}
	cfg := ExportConfig{
		Enabled:     boolVal(raw["enabled"]),
		Endpoint:    strVal(raw["endpoint"]),
		Protocol:    strVal(raw["protocol"]),
		ServiceName: strVal(raw["service_name"]),
	}
	if h, ok := raw["headers"].(map[string]string); ok {
		cfg.Headers = h
	} else if h, ok := raw["headers"].(map[string]any); ok {
		cfg.Headers = map[string]string{}
		for k, v := range h {
			cfg.Headers[k] = fmt.Sprint(v)
		}
	}
	return cfg
}

func strVal(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func boolVal(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	default:
		return false
	}
}
