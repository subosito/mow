# application-packs Specification

## Purpose
Application workflows that must not ship in stock mow, and what they are when an external host links them.

## Requirements

### Requirement: Stock mow excludes application packs
The stock `mow` binary SHALL NOT link job, ops, review, media, or focus. Doctor on that binary SHALL report configured media tools as unregistered when a host config enables them.

#### Scenario: Doctor names the missing media tools
- **GIVEN** host config that enables `understand_image` under `extensions.media`
- **WHEN** a user runs `mow doctor` on the stock binary
- **THEN** the output SHALL say the tool is not registered in this binary

### Requirement: Job is the clock
When linked by an external host, job SHALL be CLI-only (no tools, no slash): interval or cron ticks that each construct a fresh Engine. A tick MAY run a saved goal id (via mow’s goal store) or a one-shot prompt. The same id SHALL NOT overlap an in-flight tick.

#### Scenario: Goal tick uses mow goal
- **GIVEN** a linked job daemon and a saved goal `fix-ci`
- **WHEN** a schedule fires with `goal: fix-ci`
- **THEN** the tick SHALL run through mow’s goal runner
- **AND** SHALL reset a done/partial/failed goal before the tick
- **AND** SHALL skip a blocked goal until an operator answers

### Requirement: Ops is named service profiles
When linked, ops SHALL expose `ops_*` tools and an `ops` CLI over catalogs under `$MOW_HOME/ops/<name>/`. `ops_action` SHALL require shell. `ops run NAME` SHALL be a separate job daemon whose id is `ops-<name>` and SHALL NOT appear as a row in `job list`.

#### Scenario: Separate daemon
- **WHEN** an operator starts `ops run prod --every 5m`
- **THEN** tick state SHALL live at `$MOW_HOME/job/state/ops-prod.json`
- **AND** `job list` SHALL NOT include that row as a user schedule

### Requirement: Review is read-only advisory
When linked, `review` and `sec` SHALL run a two-pass advisory review with write and shell forced off and tools limited to `read`, `glob`, and `grep`. ACP `delegate` and MCP tools SHALL be denied. Slash `/review` and `/sec` SHALL use the live session engine and SHALL NOT start a model ensemble.

#### Scenario: Review jail
- **WHEN** `review` runs against a dirty worktree
- **THEN** each pass SHALL be read-only and ephemeral
- **AND** a model tool call to `bash` or `delegate` SHALL be denied

### Requirement: Media tools are config-gated
When linked, generate/understand media tools SHALL register only when a model id is configured, the name is in `tools.enable`, and an API key is resolvable. Missing media config SHALL NOT fail Engine construction. Generate/understand SHALL use the chat provider connection (`llm.*`), not a project-supplied endpoint.

#### Scenario: No config, no tools
- **GIVEN** an external host that links media but omits `extensions.media`
- **WHEN** an Engine is constructed
- **THEN** no generate_* or understand_* tools SHALL appear
- **AND** construction SHALL succeed

### Requirement: Focus remains explicit embedding
Focus SHALL remain an optional hook pack in the application module: no CLI, no tools, no slash. Default hosts SHALL NOT blank-import it. A host that does import it MAY add reread degradation, inventory ladders, a unique-file read cap, and an explore-only nag on top of stock harness guards.

#### Scenario: Default application binary
- **WHEN** a user inspects the default application-module `cmd` import list
- **THEN** focus SHALL be absent
- **AND** job, ops, review, and media SHALL be present
