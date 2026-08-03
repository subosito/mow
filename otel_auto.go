package mow

import "sync"

// OTELAutoFunc is an optional bootstrap hook that may attach OpenTelemetry
// export to a freshly built Engine. The otel subpackage registers one via
// SetOTELAuto from init when imported (stock CLI blank-imports it).
//
// cfg is a loose map of otel.* keys (endpoint, protocol, service_name,
// Returning an error fails Engine construction.
type OTELAutoFunc func(eng *Engine, cfg map[string]any) error

var (
	otelAutoMu sync.RWMutex
	otelAuto   OTELAutoFunc
)

// SetOTELAuto registers the process-wide OTEL auto-wire hook. Pass nil to
// clear. Intended for github.com/subosito/mow/otel init; hosts rarely need it.
func SetOTELAuto(fn OTELAutoFunc) {
	otelAutoMu.Lock()
	otelAuto = fn
	otelAutoMu.Unlock()
}

func runOTELAuto(eng *Engine, cfg map[string]any) error {
	otelAutoMu.RLock()
	fn := otelAuto
	otelAutoMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(eng, cfg)
}
