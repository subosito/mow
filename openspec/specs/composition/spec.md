# composition Specification

## Purpose
Which Go module and which host binary ship each surface, and how a binary is composed from blank imports.

## Requirements

### Requirement: Standalone engine module
The mow module SHALL provide a complete agent harness (Engine, policy, sessions, tools, LLM HTTP) without depending on an application module or a gateway.

#### Scenario: Library embed without a sibling checkout
- **GIVEN** an integrator who imports `github.com/subosito/mow`
- **WHEN** they construct `Engine` and call `Prompt`
- **THEN** the run SHALL talk to an OpenAI-compatible or Anthropic-compatible HTTP endpoint
- **AND** SHALL NOT require a sibling repository, host UI, or broker to be present

### Requirement: Stock binary import list
The stock `mow` binary SHALL link only the Unix CLI skeleton and the core Engine surfaces: ACP, TTY, process tools, command hooks, MCP, and goal.

#### Scenario: Help lists core extensions including goal
- **WHEN** a user runs `mow help` on the stock binary
- **THEN** the output SHALL include `mow acp`, `mow tty`, `mow mcp`, and `mow goal`
- **AND** SHALL place those commands under an Extensions group
- **AND** SHALL NOT print a Packs group

#### Scenario: Application commands are absent
- **WHEN** a user runs `mow help` on the stock binary
- **THEN** the output SHALL NOT list `mow job`, `mow ops`, `mow review`, or `mow media`

### Requirement: Blank-import composition
A host binary SHALL gain or lose a linked surface solely by including or omitting that package’s import. Removing the import SHALL remove the associated command, tools, and system-prompt segment.

#### Scenario: Drop ACP
- **GIVEN** a host that does not blank-import the ACP extension
- **WHEN** the user invokes the `acp` subcommand
- **THEN** the binary SHALL treat it as an unknown command, not as a one-shot prompt

### Requirement: Reserved unlinked command names
The CLI dispatcher SHALL refuse reserved application-command tokens even when those packs are not linked, rather than forwarding them as free-form prompts.

#### Scenario: Unlinked ops token
- **WHEN** a user runs `mow ops` on the stock binary
- **THEN** the process SHALL exit with status 2
- **AND** the error SHALL identify an unknown command named `ops`

#### Scenario: Unlinked job, review, and media tokens
- **WHEN** a user runs `mow job`, `mow review`, or `mow media` on the stock binary
- **THEN** each invocation SHALL fail as an unknown command, not as `mow run -p …`

### Requirement: Application workflows live outside the root module
Job, ops, review, media, and the optional focus hook pack SHALL live in an external application module. The mow root module SHALL NOT import that module.

#### Scenario: Stock tree has no application pack sources
- **WHEN** an integrator inspects the mow module
- **THEN** they SHALL find goal under `ext/goal`
- **AND** SHALL NOT find job, ops, review, media, or focus packages in this module

### Requirement: Factory host
A factory-style multi-step run SHALL be possible with stock `mow` (or an embed of `Engine` plus the goal extension). Recurring clocks SHALL remain an application-module concern.

#### Scenario: One-shot factory run
- **WHEN** a user runs `mow goal run --goal "Make CI green"` on the stock binary
- **THEN** the outer loop SHALL execute against the Engine without requiring the application-module binary

### Requirement: Public samples stay generic
Public documentation, CLI help, fixtures that read as examples, and OpenSpec artifacts SHALL use current public model ids and generic roles. They SHALL NOT name private host products, fleet peers, or stale model generations.

#### Scenario: Architecture and spec text
- **WHEN** a stranger reads `docs/` or `openspec/`
- **THEN** they SHALL NOT be able to infer a private fleet, host product, or in-house gateway as required infrastructure
