# harness-guards Specification

## Purpose
Stock-mow explore and WIP guards that are always on, and the focus-pack behaviors that remain out of the default host.

## Requirements

### Requirement: Harness rules always load
Every Prompt SHALL include the harness operating rules: prefer grep/glob/read; do not bash-discover; do not discard uncommitted work to make tests pass.

#### Scenario: Rules present without a project AGENTS.md
- **GIVEN** an empty workspace
- **WHEN** the Engine builds the system prompt
- **THEN** the harness rules SHALL be present
- **AND** SHALL forbid discarding uncommitted git work for a green tree

### Requirement: Bash refuses discover dumps
Unless the command is a recognized productive pipeline (tests, builds, formatters, or git write operations), `bash` SHALL refuse invocations whose primary command is `ls`, `find`, `rg`, `grep`, `egrep`, `fgrep`, `fd`, or `tree`.

#### Scenario: Bare ls is refused
- **GIVEN** shell is granted
- **WHEN** the model calls `bash` with `ls -1`
- **THEN** the tool result SHALL be an error telling the model to use glob or grep

#### Scenario: Productive pipeline still runs
- **GIVEN** shell is granted
- **WHEN** the model calls `bash` with `go test ./... | rg FAIL`
- **THEN** the command SHALL NOT be refused as a discover dump

### Requirement: Bash refuses destructive WIP discard
`bash` SHALL refuse `git restore`, `git checkout --`, `git reset --hard`, `git reset --merge`, `git clean -`, and `rm -rf` of project trees. `rm -rf` under `/tmp/` or a `$tmp` path MAY run.

#### Scenario: git restore is refused
- **GIVEN** shell is granted
- **WHEN** the model calls `bash` with `git restore .`
- **THEN** the tool result SHALL be an error that discarding uncommitted work is not allowed

#### Scenario: tmp rm is allowed
- **GIVEN** shell is granted
- **WHEN** the model calls `bash` with `rm -rf /tmp/mow-scratch`
- **THEN** the command SHALL NOT be refused by the destructive-WIP rule

### Requirement: Exploration checkpoints are advisory
After six exploration batches, two minutes of exploration, or two low-progress exploration batches, the loop SHALL inject an investigation checkpoint. The checkpoint SHALL NOT remove tools or force a final answer. Exact-repeat stall remains the only hard explore stop.

#### Scenario: Checkpoint does not abort
- **GIVEN** six consecutive explore-only batches with novel results
- **WHEN** the sixth batch completes
- **THEN** the model SHALL receive the investigation checkpoint text
- **AND** the run SHALL continue

#### Scenario: Productive work resets the streak
- **GIVEN** several explore batches
- **WHEN** a subsequent `go test` bash call succeeds
- **THEN** the exploration streak SHALL reset

### Requirement: Focus pack is not default
Stock mow SHALL NOT degrade repeated reads, ladder-refuse inventory, cap unique-file reads per prompt, or nag after N explore-only turns. Those behaviors MAY exist in an optional application-module pack that a host must blank-import explicitly.

#### Scenario: Repeated read still returns full content
- **GIVEN** stock mow with shell/write defaults
- **WHEN** the model reads the same file window twice without an intervening edit
- **THEN** the second result SHALL NOT be truncated by a focus-pack view cap

#### Scenario: Default hosts do not link focus
- **WHEN** a user inspects `cmd/mow` and the default application-module `cmd` import list
- **THEN** neither SHALL blank-import a focus pack
