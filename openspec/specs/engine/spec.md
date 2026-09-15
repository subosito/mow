# engine Specification

## Purpose
The Engine is a headless tool-calling loop: one Prompt is one inner run, with structured events for hosts.

## Requirements

### Requirement: One Prompt is one tool loop
`Engine.Prompt` SHALL run a single agent loop: model proposes tools, the host executes a batch, results return, until the model answers or a stop reason fires.

#### Scenario: Tool batch then answer
- **GIVEN** a prompt that requires a file read
- **WHEN** the model returns a `read` tool call
- **THEN** the Engine SHALL execute it and append the result
- **AND** SHALL continue the loop until a final assistant text or a stop reason

### Requirement: Extra tools are per Prompt
Tools supplied as `PromptOpts.ExtraTools` SHALL be available only for that Prompt. They SHALL NOT remain in the Engine’s global tool set after the Prompt returns.

#### Scenario: Goal step tools do not leak
- **GIVEN** a goal step that injects `goal_report` via ExtraTools
- **WHEN** that step’s Prompt ends
- **THEN** a subsequent ordinary Prompt on the same Engine SHALL NOT advertise `goal_report`

### Requirement: Event layers
The Engine SHALL emit structured events with a stable layer prefix: `loop.*` for turn lifecycle, `graph.*` for goal orchestration, `harness.*` for tools and peer diagnostics.

#### Scenario: Goal events exist on the Engine
- **WHEN** a linked goal runner starts, steps, or finishes a goal
- **THEN** hosts SHALL observe `graph.goal.start`, `graph.goal.step`, and a terminal `graph.goal.done`, `graph.goal.fail`, `graph.goal.partial`, or `graph.goal.blocked`

### Requirement: Parallel tool cap
A single assistant tool batch SHALL run at most `policy.max_parallel_tools` calls concurrently. The default SHALL be 8.

#### Scenario: Default cap
- **GIVEN** a host that does not set `max_parallel_tools`
- **WHEN** the model returns more than eight tool calls in one batch
- **THEN** the Engine SHALL not execute more than eight of them at the same time

### Requirement: Committed history is tag-free
Chain-of-thought markup such as `<think>` SHALL be stripped before it is committed to session history.

#### Scenario: Think tags do not persist
- **WHEN** the model emits a `<think>` block plus an answer
- **THEN** the stored session transcript SHALL contain the answer without the think tags

### Requirement: Stall backstop
The loop SHALL stop with `StopStuck` after three consecutive tool batches that add no new evidence (same tool, args, and result hash). This backstop SHALL NOT be a user-facing config knob.

#### Scenario: Three barren batches
- **GIVEN** three consecutive batches whose complete results were already seen this run
- **WHEN** the third barren batch completes
- **THEN** the run SHALL end with stop reason `stuck`
- **AND** SHALL emit `loop.stall`
