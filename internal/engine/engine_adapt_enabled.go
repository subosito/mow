package engine

// Enabled forwards optional Enabled(*Engine) so conditionally registered
// extension and per-engine tools are filtered for the current engine.
func (a toolAdapter) Enabled(e *Engine) bool {
	type enabled interface{ Enabled(*Engine) bool }
	if x, ok := a.t.(enabled); ok {
		return x.Enabled(e)
	}
	return true
}
