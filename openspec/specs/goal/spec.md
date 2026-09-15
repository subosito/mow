# goal Specification

## Purpose
Goal is the stock outer loop over Engine.Prompt: durable checklist state, budgets, and per-step tools, without a config section.

## Requirements

### Requirement: Goal is a core extension
The goal package SHALL register as Layer `ext` and SHALL be blank-imported by the stock `mow` binary. It SHALL NOT require an `extensions.goal` config section.

#### Scenario: Stock help lists goal as an extension
- **WHEN** a user runs `mow help`
- **THEN** `mow goal` SHALL appear under Extensions
- **AND** SHALL NOT appear under Packs

### Requirement: Durable store
Goal state SHALL persist under `$MOW_HOME/goals`. Creating, running, listing, inspecting, resetting, and deleting goals SHALL be possible via `mow goal` subcommands `new`, `run`, `list`, `status`, `reset`, and `delete`.

#### Scenario: Create then status
- **WHEN** a user runs `mow goal new --id fix-ci --goal "Make CI green"`
- **THEN** a goal record SHALL exist under `$MOW_HOME/goals`
- **AND** `mow goal status --id fix-ci` SHALL report that id and the stored budget

### Requirement: Outer Prompt budget
The default outer Prompt budget SHALL be 16 steps. A Spec SHALL NOT request more than 64. `--max-steps` on resume MAY raise a stored budget up to that ceiling so a max-steps failure can continue.

#### Scenario: Default budget
- **WHEN** a goal is created without `--max-steps`
- **THEN** its stored max_steps SHALL be 16

#### Scenario: Ceiling
- **WHEN** a caller requests more than 64 steps
- **THEN** the stored budget SHALL be capped at 64

### Requirement: Step tools are injected, not global
`goal_report` and goal-scoped process tools (`goal_process_*`) SHALL be attached only through `PromptOpts.ExtraTools` for the in-flight step.

#### Scenario: Ordinary run does not see goal_report
- **GIVEN** stock mow with goal linked
- **WHEN** a user runs `mow run -p "hello"`
- **THEN** the tool list advertised to the model SHALL NOT include `goal_report`

### Requirement: Checklist gate on done
`goal_report` with status `done` SHALL be rejected when a checklist exists and any item is not done or skipped. The step SHALL continue instead of finishing the goal.

#### Scenario: Incomplete checklist
- **GIVEN** a goal whose plan still has an open item
- **WHEN** the model calls `goal_report` with status `done`
- **THEN** the goal SHALL remain in progress
- **AND** the model SHALL be told the checklist is incomplete

### Requirement: Slash command
When the TTY or ACP surface is linked, a `/goal` slash command SHALL drive list/status/run against the current session Engine and SHALL stream `graph.goal.*` events.

#### Scenario: Slash is exclusive
- **WHEN** `/goal` is invoked mid-turn
- **THEN** the host SHALL refuse it as an exclusive slash command rather than interleaving it with the in-flight Prompt
