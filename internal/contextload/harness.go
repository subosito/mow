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

Workspace and path jail
- File tools (read, glob, grep, write, edit) are path-jailed: only the workspace and any host-configured extra roots.
- Relative paths resolve against the workspace. Absolute paths are allowed when they fall under the workspace or an extra root.
- When the system lists extra roots, use those absolute paths to read or edit there — do not refuse as "outside workspace".
- Follow project instruction files (AGENTS.md, CLAUDE.md, skills) when present. They describe this repo; do not invent other products or hosts.

Tools
- Prefer read, grep, glob, edit, write for file work and discovery.
- Use bash for git, tests, builds, and one-off commands — not as the main way to read source (avoid cat/sed/head/tail/wc/ls/rg loops over the tree). Bash output is the usual context bloat: git diff bodies and listings re-enter history.
- Use proc_start / proc_status / proc_stop for long-lived processes (dev servers, watchers, mocks). Requires --allow-shell. Do NOT use bash & or leave a server in the foreground: the bash tool kills its process group when it returns.
- Do not paste raw rg/ls/find dumps into the reply; cite paths and edit.
- Do not re-read or re-cat the same paths without an intervening edit or a new user question.
- Do not nest another agent loop, outer goal runner, or recursive peer delegate unless the user asks.

Context cost
- Every user turn re-sends the full conversation. When context is large, short acknowledgments (thanks, ok, hi, please rebuild alone) are expensive full-context round trips — batch status with the next real instruction.
- Prefer fewer, denser turns over many status pings. Combine "commit + rebuild + next step" into one message when possible.
- After a peer/subagent reply, fold it into a short summary (≤10 lines) plus a file pointer; do not keep the raw exchange in the parent context.
- When context feels large: explore less, act more; avoid re-surveying paths already known.

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

// PathJailFacts is a short system segment listing the workspace and any extra
// FS roots so the model uses absolute paths under extra roots instead of
// refusing them as "restricted".
func PathJailFacts(workspace string, extraRoots []string, extraRootsReadOnly ...[]string) string {
	ws := strings.TrimSpace(workspace)
	var roRoots []string
	if len(extraRootsReadOnly) > 0 {
		roRoots = extraRootsReadOnly[0]
	}
	if ws == "" && len(extraRoots) == 0 && len(roRoots) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Path jail (read/glob/grep/write/edit):\n")
	if ws != "" {
		b.WriteString("- Workspace (relative paths resolve here): ")
		b.WriteString(ws)
		b.WriteByte('\n')
	}
	if len(extraRoots) == 0 && len(roRoots) == 0 {
		b.WriteString("- No extra roots; absolute paths must stay under the workspace.")
		return b.String()
	}
	if len(extraRoots) > 0 {
		b.WriteString("- Extra roots (use absolute paths under these — read and write allowed):\n")
		for _, r := range extraRoots {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			b.WriteString("  - ")
			b.WriteString(r)
			b.WriteByte('\n')
		}
	}
	if len(roRoots) > 0 {
		b.WriteString("- Read-only extra roots (use absolute paths — read allowed, write/edit denied):\n")
		for _, r := range roRoots {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			b.WriteString("  - ")
			b.WriteString(r)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
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
