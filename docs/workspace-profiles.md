# Rich workspace profiles

> **Status:** design and acceptance contract for profile migration (phase 1) and
> profile ACP peers (phase 2). This replaces the legacy
> `$MOW_HOME/workspaces.yaml` workspace-set file.

A workspace profile is operator-owned state below `$MOW_HOME`:

```text
$MOW_HOME/workspaces/<name>/
  workspace.yaml  # required: primary root and optional extra roots
  config.yaml     # optional profile configuration
  AGENTS.md       # optional profile instructions
  skills/         # optional profile skills: <skill-name>/SKILL.md
```

`<name>` is an identifier, not a path. It must be non-empty, trimmed, and use
only a conservative filename-safe form (`[A-Za-z0-9][A-Za-z0-9_-]*`). Reject
path separators (including `\\`), dot segments, whitespace padding, and any
other value that could escape `$MOW_HOME/workspaces`. A literal directory path
remains valid for one-shot callers; it is never interpreted as a profile name.

## Resolution and precedence

When `--workspace NAME` is a valid profile name and
`$MOW_HOME/workspaces/NAME/workspace.yaml` exists, the profile supplies the
primary workspace and its `extra_roots`. Relative profile roots resolve from
the profile's primary workspace; `:ro` remains supported. A profile must not
read, merge, or fall back to legacy `workspaces.yaml`.

Configuration precedence remains explicit and deterministic:

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

The profile is operator-owned, so its `config.yaml` may express the same
non-secret operational settings as user configuration. It **must not** become
a credential or endpoint injection mechanism: credentials, `llm.base_url`,
custom headers, session directories, and other security-sensitive settings
continue to follow the existing user/host restrictions. A project checkout
remains untrusted unless trust is granted out of band.

Instruction sources are additive. The profile `AGENTS.md` is loaded as
operator instructions before workspace instructions; a workspace cannot
replace it. Skill directory precedence is:

```text
$MOW_HOME/skills → configured skills.dirs → profile skills → trusted workspace/.mow/skills
```

Skills deduplicate by normalized skill name, with the first directory winning.
This gives a profile operator the ability to pin a skill without allowing a
repository clone to shadow it.

## ACP peers (phase 2)

`config.yaml` may contain `extensions.acp.agents` and `mow_agents`. Those peers
are scoped to the Engine that selected the profile. They must be unavailable to
an Engine using another profile or a plain workspace path, even if both Engines
exist concurrently in the same process. Registration must not mutate a package
singleton that accumulates agents or captures the first Engine's workspace.

Native `mow_agents` inherit the selecting Engine's effective capabilities by
default: workspace, extra-root jail, and write/shell permissions. An agent can
only be made **more restrictive** by profile configuration; it must not gain
write, shell, roots, credentials, or a different workspace merely because its
profile declares a peer. Delegated processes receive only the narrowed effective
options and use the selecting Engine's workspace as their default cwd. Peer pool
reuse is keyed by agent + cwd + built argv (model/flags) so a capability change
cannot reuse a more-privileged process. Host `--read-only` with explicit
`:rw` writable roots is not yet mirrored as peer `WritableRoots` (see remaining
risks in peer-delegation audits).

## Acceptance coverage

Tests must cover name rejection before path resolution, profile root/`:ro`
resolution, no legacy fallback, configuration and instruction/skill precedence,
and ACP isolation. ACP tests should construct two Engines with distinct profile
agents and capabilities, assert each tool sees only its own agents and defaults,
and prove closing one Engine cannot alter the other. Run them with `-race` to
catch global registration or peer-pool leakage.
