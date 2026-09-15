# core-extensions Specification

## Purpose
Detachable Engine surfaces in `ext/`: how the stock host is driven, without application-domain workflows.

## Requirements

### Requirement: CLI skeleton
The CLI package SHALL own `run`, `trust`, `doctor`, `approvals`, `models`, `version`, and `help`. It SHALL dispatch unknown non-reserved args as `run -p …`. No-args on a TTY SHALL invoke the registered default interactive command when one exists.

#### Scenario: Free-form prompt
- **WHEN** a user runs `mow summarize this repo`
- **THEN** the dispatcher SHALL handle it as a one-shot `run` prompt

#### Scenario: No-args TTY
- **GIVEN** stdout is a TTY and TTY is linked as the default interactive command
- **WHEN** a user runs `mow` with no args
- **THEN** the TTY REPL SHALL start

### Requirement: TTY is optional
The TTY extension SHALL register `mow tty` as a line REPL. It SHALL NOT import the ACP package. Dropping the import SHALL remove the command.

#### Scenario: In-session slash
- **WHEN** a user types `/help` inside `mow tty`
- **THEN** the REPL SHALL list built-in slash commands plus any registered ones

### Requirement: ACP agent and delegate
The ACP extension SHALL expose `mow acp` as an Agent Client Protocol agent and a `delegate` tool for named peers. Peer failures SHALL surface as tool results, not as a parent-session abort. Native peers SHALL inherit host write/shell grants capped by the host; they SHALL NOT receive credentials on argv.

#### Scenario: Peer failure is non-fatal
- **GIVEN** a configured peer that exits on spawn
- **WHEN** the model calls `delegate`
- **THEN** the parent Prompt SHALL continue
- **AND** the tool result SHALL include a redacted peer-stderr snippet

### Requirement: MCP is config-gated
MCP client tools SHALL exist only when servers are configured. `mow mcp` SHALL expose `mow_prompt` as an MCP stdio server. No MCP config SHALL mean no client subprocess.

#### Scenario: Empty config
- **GIVEN** host config with no `extensions.mcp.mcpServers`
- **WHEN** an Engine is constructed
- **THEN** no MCP client process SHALL start

### Requirement: Process tools have no CLI
The proc extension SHALL register `proc_start`, `proc_status`, and `proc_stop` only. Session-scoped processes SHALL die on `Engine.Close` unless `keep` is true.

#### Scenario: Close reaps
- **GIVEN** a `proc_start` without `keep`
- **WHEN** the Engine closes
- **THEN** that process group SHALL receive SIGTERM then SIGKILL

### Requirement: Command hooks come from host-owned plugins
Command-hook lifecycle scripts SHALL load from host-owned Agent Plugins (`$MOW_HOME/plugins` and workspace-profile `plugins/`) that ship `hooks/hooks.json`. There SHALL NOT be an `extensions.cmdhook` section. Project `.mow/plugins` SHALL be skills-only.

#### Scenario: No config section
- **WHEN** a host omits any `extensions.cmdhook` key
- **THEN** installed host plugins that ship `hooks/hooks.json` SHALL still register hooks on `BeforeNew`

### Requirement: System-prompt segments follow the link set
Extensions that contribute system-prompt advice SHALL do so through registration so the model only sees guidance for tools the binary actually linked.

#### Scenario: Unlinked proc
- **GIVEN** a host that does not import proc
- **WHEN** the system prompt is built
- **THEN** it SHALL NOT tell the model to use `proc_start`
