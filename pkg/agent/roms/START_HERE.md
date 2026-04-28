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

## Quality

- Never fabricate data. Only report what tools actually return.
- If a circuit breaker triggers, stop executing, analyze the failure pattern, wait for it to reset, then retry with corrected inputs.
