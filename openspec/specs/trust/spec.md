# trust Specification

## Purpose
Secure-by-default tool access: read-only until the host grants write or shell, with a path jail that project config cannot expand.

## Requirements

### Requirement: Default tools are read-only
A new Engine SHALL enable `read`, `glob`, and `grep` by default. Write, edit, and bash SHALL require an explicit host or CLI grant (`--allow-write` / `--allow-shell` or the equivalent host config).

#### Scenario: Fresh engine cannot write
- **GIVEN** default options and no write grant
- **WHEN** the model calls `write`
- **THEN** the tool SHALL be denied

### Requirement: File tools are path-jailed
`read`, `glob`, `grep`, `write`, and `edit` SHALL only operate on the workspace and host-configured extra roots. Extra roots SHALL be fixed at Engine construction and SHALL NOT be settable from project config.

#### Scenario: Path outside the jail
- **GIVEN** workspace `/proj` and no extra roots
- **WHEN** the model reads `/etc/passwd`
- **THEN** the tool SHALL fail as outside the jail

### Requirement: Bash is not path-jailed
The `bash` tool SHALL NOT inherit the filesystem path jail. Enabling it SHALL be the host’s trust decision (`--allow-shell` or host config).

#### Scenario: Shell grant is explicit
- **GIVEN** default options
- **WHEN** the model calls `bash`
- **THEN** the tool SHALL be denied until the host grants shell

### Requirement: Project config cannot escalate
A workspace `.mow/config.yaml` SHALL NOT set credentials, `llm.base_url`, headers, provider catalogs, `session.dir`, power tools, `extra_roots`, `env_passthrough`, sandbox mode, spend ceilings, native tools, media or ACP extension sections, `llm.model` / `effort` / `stream`, `skills.selector`, or a `max_turns` that raises the host ceiling.

#### Scenario: Project tries to set base_url
- **GIVEN** a trusted workspace whose project config sets `llm.base_url`
- **WHEN** the Engine loads config
- **THEN** the host/user `base_url` SHALL remain in force

#### Scenario: Project tries to enable bash
- **GIVEN** a trusted workspace whose project config lists `bash` in `tools.enable`
- **WHEN** the Engine loads config
- **THEN** bash SHALL remain disabled unless the host already granted it

### Requirement: Trust is out-of-band
Workspace trust SHALL be stored under `$MOW_HOME/trusted` (or `mow trust`), never as a marker file in the repository.

#### Scenario: Clone does not self-trust
- **GIVEN** a freshly cloned workspace with no out-of-band trust entry
- **WHEN** the Engine loads user config
- **THEN** project config SHALL NOT load

### Requirement: Instruction files do not escape the git root
AGENTS.md / CLAUDE.md SHALL be read from the workspace up to the enclosing git root (or `$HOME` when there is no git root), and never above. Hermetic engines (`LoadUserConfig=false`) SHALL read the workspace only.

#### Scenario: Hermetic engine
- **GIVEN** `LoadUserConfig` is false
- **WHEN** the Engine loads instructions
- **THEN** it SHALL NOT read instruction files above the workspace

### Requirement: Secrets stay out of subprocesses and logs
Shell children (`bash`, `proc_start`) SHALL NOT inherit `*_API_KEY` environment variables unless the name is listed in host-only `policy.env_passthrough`. Logs and tool diagnostics SHALL redact secrets.

#### Scenario: Default env scrub
- **GIVEN** the host environment contains `OPENAI_API_KEY`
- **WHEN** a granted `bash` command runs `env`
- **THEN** the child environment SHALL omit `OPENAI_API_KEY`
