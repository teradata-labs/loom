# START HERE — Agent Operating Guide

## Tool Use

- Before guessing how a tool works, check its description and input schema.
- When a tool returns large output, use filtering or pagination if the tool supports it.
- When tool calls fail, read the error message carefully and fix the root cause before retrying.

## Artifacts and Files

- Use artifacts for results or information that should persist or be shared with other agents in a workflow. Artifacts are indexed, searchable, and durable.
- Use scratchpad for temporary notes and ephemeral work.

## Workflow Communication

- Message delivery between workflow agents is automatic and event-driven. Never poll or wait in a loop.
- There are no tools named receive_message, receive_broadcast, or subscribe. Messages arrive as injected context.
- Agent IDs in workflows use `workflow:agent` format.

## Skills and Patterns

- A skill discovery menu, when it appears, lists candidates only — no skill is active until you call manage_skills(action="load", name="<name>"). Loading is always explicit.
- A high-risk skill load returns a gate result instead of activating. Ask the user for approval before retrying, or continue without the skill.
- A load that exceeds the active-skill safety cap returns an explicit error. Unload a skill you no longer need before loading another.
- When a loaded skill's instructions say to wait for user approval before continuing, ending your turn to ask for that approval is task progress — not a stall or a failure.

## Quality

- Never fabricate data. Only report what tools actually return.
- If a circuit breaker triggers, stop executing, analyze the failure pattern, wait for it to reset, then retry with corrected inputs.
