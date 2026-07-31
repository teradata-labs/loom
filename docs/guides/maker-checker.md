# Maker/Checker Verification

How to pair a producing agent (maker) with a verifying agent (checker) so artifacts are judged against explicit criteria before they are trusted.

**Version**: v1.4.0 (unreleased)

---

## When to use which mechanism

Loom has three verification layers. Pick the cheapest one that can reject your output:

| Mechanism | Verifier | Blocks? | Use when |
|---|---|---|---|
| Stage `output_schema` | JSON Schema (deterministic, free) | Yes — stage retries with feedback | The contract is structural |
| Checker agent (this guide) | A second LLM with different instructions | Pipeline variant: yes. Spawn variant: no (advisory) | The contract is judgment ("is this migration safe?") |
| `hitl_gate` | A human | Yes — durable suspend/resume | The decision is irreversible or high-stakes |

Prefer deterministic verifiers first: a schema can reject bad output for free; an LLM checker costs a conversation and can be wrong. Use a checker agent for criteria a schema cannot express, and keep its verdict itself schema-enforced so the checker cannot ramble.

---

## The verifier template

`examples/reference/agent-templates/verifier.yaml` ships a pure-checker template:

- **Parameters**: `artifact_description` (what it judges), `verification_criteria` (the checklist), `output_contract` (optional structural constraints).
- **Strict verdict contract** — the checker must return exactly:

```json
{
  "passed": false,
  "critique": "one-paragraph summary",
  "violations": [
    {"criterion": "…", "severity": "BLOCKER|MAJOR|MINOR", "detail": "…"}
  ]
}
```

- **Checker discipline**: `temperature: 0.1`, `max_turns: 3`, and **no tools** — a checker reads and judges; it does not act. (Note: `max_tool_executions: 0` cannot express "no tools"; proto3 zero values are rewritten to the default of 50. The template simply declares no tools, and inherits none.)

Instantiate it:

```go
registry := orchestration.NewTemplateRegistry()
_ = registry.LoadTemplate("agent-templates/base-expert.yaml")
_ = registry.LoadTemplate("agent-templates/verifier.yaml")

config, err := registry.ApplyTemplate("verifier", map[string]string{
    "artifact_description":  "a SQL migration script",
    "verification_criteria": "- reversible\n- non-destructive\n- indexed for live-row filtering",
})
```

---

## Variant 1: pipeline stage (synchronous gating)

Use when the verdict must gate the workflow. See `examples/reference/workflows/orchestration-patterns/maker-checker-pipeline.yaml`:

1. Stage 1 (`sql-maker`) produces the artifact.
2. Stage 2 (`verifier`) judges it. The stage's `output_schema` enforces the verdict JSON deterministically, and `retry_policy` retries the checker with feedback if the verdict is malformed.

The pipeline blocks on the checker, and downstream stages (or your caller) read `passed`/`violations` from clean JSON.

For **automated regeneration loops** (checker fails → maker retries), use the iterative pipeline pattern. For **human-gated revision**, attach a `hitl_gate` to the checker stage.

---

## Variant 2: ephemeral spawn (advisory checks)

Use when the parent should keep working while the check runs. The parent calls `manage_ephemeral_agents`:

```json
{
  "command": "spawn",
  "agent_id": "verifier",
  "auto_subscribe": ["verify.migration.42"],
  "initial_message": "Verify this artifact:\n\n<the artifact>"
}
```

Semantics (as of this version):

- `initial_message` is **delivered** to the first successfully subscribed `auto_subscribe` topic, with the parent as sender, immediately at spawn. Previously it was silently stored in metadata; `initial_message` without `auto_subscribe` is now rejected (`initial_message requires auto_subscribe`).
- The spawned checker processes the message asynchronously and **publishes its verdict back to the same topic**. The parent (subscribed to the topic) receives it as an auto-injected message.
- This is **advisory**: the parent is not blocked, and nothing enforces that the parent honors the verdict. Use the pipeline variant when the verdict must gate.

Known limitations (pre-existing, documented so you don't design around phantom capabilities):

- The spawned-agent loop services **only its first subscription**; deliver work to `auto_subscribe[0]`.
- Spawn cap: 10 sub-agents per parent session; idle sub-agents auto-despawn after 15 minutes.
- The `preset` spawn parameter and `EphemeralAgentPolicy` template resolution are not wired in the CLI server path — spawn registered agents by `agent_id`.

---

## Verdict handling rules

- Treat a missing or malformed verdict as **inconclusive, not failed** — retry the checker or escalate; never silently pass.
- `passed: true` with a non-empty `violations` array is a checker contract violation; treat as inconclusive.
- Log verdicts with the artifact identity so failed checks are auditable.

## See also

- `docs/reference/agent-configuration.md` — agent-loop `behavior.output_policy` (self-verification without a second agent)
- `examples/reference/workflows/orchestration-patterns/hitl-gated-pipeline.yaml` — human gating
- `docs/architecture/loop-engineering.md` — where maker/checker sits in the loop-engineering roadmap
