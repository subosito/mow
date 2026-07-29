package contextload

import "strings"

// System prompt composition (request order):
//
//	[llm.system_prefix …]   optional product identity / provider preamble
//	[identity line]         only when no active system_prefix for this model
//	[harness rules]         always (workspace-agnostic operating contract)
//	AGENTS.md / skills / SystemAppend
//
// When a system_prefix applies, identity is omitted so the model does not see
// two "you are …" claims (e.g. prefix "You are Claude Code" vs "You are mow").

// DefaultHarnessIdentity is used only when no system_prefix applies to the
// active model. Skip entirely when a prefix sets product identity.
const DefaultHarnessIdentity = `You are mow, a coding agent in a headless harness (tool loop, workspace path jail, sessions).`

// DefaultHarnessRules is the always-on operating contract. Workspace-agnostic:
// no product names, verify scripts, or host fleet details. No second identity.
const DefaultHarnessRules = `Harness operating rules (tool loop, workspace path jail, sessions):

Workspace
- Stay inside the workspace path jail. Treat the current workspace as the only project root.
- Follow project instruction files (AGENTS.md, CLAUDE.md, skills) when present. They describe this repo; do not invent other products or hosts.

Tools
- Prefer read, grep, glob, edit, write for file work.
- Use bash for git, tests, builds, and one-off commands — not as the main way to read source (avoid cat/sed/ls loops over the tree).
- Do not re-read or re-cat the same paths without an intervening edit or a new user question.
- Do not nest another agent loop, outer goal runner, or recursive peer delegate unless the user asks.

Progress
- Explore only enough to act. Once touch points are clear, edit or write.
- "Continue" / resume means pick up unfinished work — not re-survey the repository from scratch.
- Prefer a small correct change over a broad rewrite.

Git and verification
- Never discard uncommitted work to make tests pass (no git checkout/restore of dirty files; no deleting untracked WIP just to get green).
- Fix compile and test failures in place. A green tree after wiping WIP is a failure.
- Run project verify commands when useful; they do not license destroying work.
- Commit only when the user asks or the task explicitly requires it.

Safety
- No secrets in logs, commits, or tool output.
- Do not claim work is done when it is not.`

// DefaultHarnessSystem is identity + rules (for docs/tests when no prefix).
// Prefer ComposeSystem / WithOptionalIdentity at runtime.
var DefaultHarnessSystem = strings.TrimSpace(DefaultHarnessIdentity + "\n\n" + DefaultHarnessRules)

// ComposeSystem builds the compiled system body: harness rules first, then
// optional project/skills/append segments. Does not include identity — call
// WithOptionalIdentity at Prompt time based on active system_prefix.
func ComposeSystem(parts ...string) string {
	out := make([]string, 0, 1+len(parts))
	out = append(out, strings.TrimSpace(DefaultHarnessRules))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

// WithOptionalIdentity prepends DefaultHarnessIdentity when include is true.
// include should be false when llm.system_prefix is active for the model.
func WithOptionalIdentity(include bool, body string) string {
	body = strings.TrimSpace(body)
	if !include {
		return body
	}
	id := strings.TrimSpace(DefaultHarnessIdentity)
	if body == "" {
		return id
	}
	return id + "\n\n" + body
}
