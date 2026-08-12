package review

// Read-only review tool policy constants. Review/sec always run PromptOpts.ReadOnly
// with CLI write/shell disabled; the engine exposes only builtins (read/glob/grep)
// plus extension tools that declare ReadOnly() true. Mis-declared extension tools
// are blocked at call time, not removed from registration — see docs/review.md.
const readOnlyToolPolicy = "prompt_read_only"

// stampReadOnlyRun records the enforced read-only posture on the report envelope.
func stampReadOnlyRun(run *RunInfo) {
	if run == nil {
		return
	}
	run.ReadOnly = true
	run.ToolPolicy = readOnlyToolPolicy
}
