# Workspace profiles

Named operator-owned state under `$MOW_HOME/workspaces/<name>/`. This replaced
the flat `$MOW_HOME/workspaces.yaml` registry.

```text
$MOW_HOME/workspaces/<name>/
  workspace.yaml  # optional: primary root and extra_roots
  config.yaml     # optional overlay (same privilege as global config.yaml)
  AGENTS.md       # optional profile instructions
  skills/         # optional profile skills
  plugins/        # optional Agent Plugins (MCP + hooks from host-owned roots)
```

`<name>` is an identifier, not a path: non-empty, trimmed,
`[A-Za-z0-9][A-Za-z0-9_-]*`. Reject path separators, `..`, and whitespace
padding. A literal directory path is still a one-shot workspace; it is never a
profile name.

A profile directory is enough to exist: `workspace.yaml` is optional
(config-only / plugins-only profiles keep the caller’s default root).
`--workspace NAME` loads `$MOW_HOME/workspaces/NAME/` when that directory
exists. No fallback to legacy `workspaces.yaml`. Relative roots resolve from
the profile’s primary workspace; `:ro` remains supported.

## Precedence

```text
defaults
  → $MOW_HOME/config.yaml
  → profile config.yaml
  → explicit --config paths
  → environment
  → trusted project .mow/config.yaml (restricted)
  → environment (re-applied)
  → explicit Options / CLI flags
```

The overlay is operator-owned `$MOW_HOME` state: it may set the same knobs as
global `config.yaml` (including `extensions.acp`). Project checkout config
stays restricted and untrusted unless granted out of band (`mow trust`).

Instruction sources are additive. Profile `AGENTS.md` loads before workspace
instructions; a workspace cannot replace it. Skill directory precedence:

```text
$MOW_HOME/skills → skills.dirs → profile skills → plugin skills → trusted workspace/.mow/skills
```

Dedup is by skill name; first directory wins, so a clone cannot shadow a
profile pin.

## ACP peers

`config.yaml` may contain `extensions.acp.agents` and `mow_agents`. Those peers
are scoped to the Engine that selected the profile — not visible to another
profile or a plain workspace path, even in the same process.

Native `mow_agents` inherit the selecting Engine’s workspace, extra-root jail,
and write/shell flags. A profile may only **narrow** those; it must not grant
write, shell, roots, credentials, or a different workspace. Peer pool reuse is
keyed by agent + cwd + argv so a capability change cannot reuse a more
privileged process.
