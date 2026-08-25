package review

import "github.com/subosito/mow"

// Read-only review tool policy constants. Review/sec run PromptOpts.ReadOnly with
// PromptOpts.AllowedTools limited to mow.BuiltinReadInspectTools (read/glob/grep
// only). Extension, MCP, and ACP tools are omitted from specs and denied at exec.
const readOnlyToolPolicy = "builtin_read_inspect_only"

// applyReviewEngineIsolation configures candidate and verifier engines so
// constructing them does not run extension BeforeNew setup (MCP/cmdhook
// processes) or inherit extension lifecycle hooks. User LLM config still loads;
// extensions.review budgets are read by loadConfig before scope resolve.
func applyReviewEngineIsolation(opt *mow.Options) {
	if opt == nil {
		return
	}
	opt.SkipExtensionSetup = true
	opt.DisableExtensionHooks = true
}

// stampReadOnlyRun records the enforced read-only posture on the report envelope.
func stampReadOnlyRun(run *RunInfo) {
	if run == nil {
		return
	}
	run.ReadOnly = true
	run.ToolPolicy = readOnlyToolPolicy
}
