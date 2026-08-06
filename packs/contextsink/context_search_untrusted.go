package contextsink

// Untrusted marks recovered content as external data. Stored tool bodies are
// saved before the agent loop adds its provenance frame, and archive snippets
// may quote external tool output; both must be framed again on recovery.
func (t *contextSearchTool) Untrusted() bool { return true }
